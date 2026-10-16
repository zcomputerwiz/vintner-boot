#!/bin/bash
# Broken root recovery fallback test (WO-0004).
#
# Tests that deliberate corruption of a Debian root partition on both UEFI
# and BIOS firmwares leads to Recovery A after exactly 3 failed boots,
# and verifies that Recovery A does not alter the root partition.
#
# Deliverables:
#   ci/recovery-fallback/run.sh <uefi|bios>
#   ci/recovery-fallback/README.md
#
# Requires: root via sudo, KVM, loop devices with partition scanning,
# debugfs (e2fsprogs), mtools, qemu-system-x86, qemu-utils, cloud-image-utils,
# sfdisk, python3, python3-virt-firmware, golang-go.
set -euo pipefail

if [ $# -ne 1 ] || { [ "$1" != "uefi" ] && [ "$1" != "bios" ]; }; then
	echo "Usage: $0 {uefi|bios}" >&2
	exit 1
fi

FIRMWARE="$1"
cd "$(dirname "$0")/../.."

OUT="ci/out/recovery-fallback/$FIRMWARE"
MNT="$OUT/mnt"
RESULTS="$OUT/results.tsv"
export MTOOLS_SKIP_CHECK=1

mkdir -p "$OUT" "$MNT"

# --- Cleanup handler --------------------------------------------------------
LOOP=""
cleanup() {
	local rc=$?
	if [ -n "${MNT:-}" ] && mountpoint -q "$MNT"; then
		sudo umount "$MNT" || true
	fi
	if [ -n "${LOOP:-}" ]; then
		sudo losetup -d "$LOOP" || true
	fi
	if [ -f "src/gen_manifest.go" ]; then
		rm -f src/gen_manifest.go || true
	fi
	if [ "$rc" != 0 ]; then
		echo "=== Failure: dumping last 60 lines of each boot log ==="
		for l in "$OUT"/*.log; do
			[ -f "$l" ] && { echo "--- last 60 lines of $l ---"; tr -d '\r' < "$l" | tail -n 60; }
		done
	fi
}
trap cleanup EXIT

# --- 1. Base image build via shared library ---------------------------------
./ci/lib/vintner-base.sh "$FIRMWARE" "$OUT"
# shellcheck source=/dev/null # written by vintner-base.sh
. "$OUT/base.env"
# shellcheck source=ci/lib/vintner-qemu.sh
. ci/lib/vintner-qemu.sh

# --- 2. Build UEFI test recovery UKIs if UEFI --------------------------------
TEST_RECA_UKI="$OUT/vintner-recovery-a-test.efi"
TEST_RECB_UKI="$OUT/vintner-recovery-b-test.efi"
if [ "$FIRMWARE" = "uefi" ]; then
	echo "=== Building UEFI test recovery UKIs ==="
	OSREL_A="recovery/out/osrel-a"
	OSREL_B="recovery/out/osrel-b"
	if [ ! -f "$OSREL_A" ]; then
		OSREL_A="$OUT/osrel-a"
		printf 'ID=debian\nIMAGE_ID=vintner-recovery-a\nIMAGE_VERSION=test\nPRETTY_NAME="vintner recovery A (test)"\n' > "$OSREL_A"
	fi
	if [ ! -f "$OSREL_B" ]; then
		OSREL_B="$OUT/osrel-b"
		printf 'ID=debian\nIMAGE_ID=vintner-recovery-b\nIMAGE_VERSION=test\nPRETTY_NAME="vintner recovery B (test)"\n' > "$OSREL_B"
	fi

	python3 "$UKIFY" build \
		--linux="recovery/out/vmlinuz" \
		--initrd="recovery/out/initrd.img" \
		--stub="$STUB" \
		--os-release="@$OSREL_A" \
		--cmdline="console=tty0 console=ttyS0,115200 vintner.recovery=1 vintner.recovery_test=1" \
		--output="$TEST_RECA_UKI"

	python3 "$UKIFY" build \
		--linux="recovery/out/vmlinuz" \
		--initrd="recovery/out/initrd.img" \
		--stub="$STUB" \
		--os-release="@$OSREL_B" \
		--cmdline="console=tty0 console=ttyS0,115200 vintner.recovery=1 vintner.recovery_test=1 vintner.test_slot=b" \
		--output="$TEST_RECB_UKI"
fi

# --- 3. Cloud-init seed disk ------------------------------------------------
SEED_IMG="$OUT/seed.img"
mkdir -p "$OUT/cidata"
cat <<EOF > "$OUT/cidata/meta-data"
instance-id: vintner-fallback-test
local-hostname: debian-vintner-fallback
EOF
cat <<EOF > "$OUT/cidata/user-data"
#cloud-config
disable_root: false
network:
  config: disabled
EOF
cloud-localds "$SEED_IMG" "$OUT/cidata/user-data" "$OUT/cidata/meta-data"

# --- 4. Helper functions ----------------------------------------------------
clean_ansi() {
	sed -r 's/\x1b\[[0-9;:]*[a-zA-Z]//g' | tr -d '\r'
}

get_booted_image() {
	local log="$1"
	local fw="$2"

	local cmdline
	cmdline=$(clean_ansi < "$log" | grep -m1 'Command line: ' | sed 's/.*Command line: //' || true)
	if [ -z "$cmdline" ]; then
		echo "none"
		return
	fi

	# Whole words of the command line; "vintner.bootstorage=" is matched as a prefix.
	local words=" $cmdline "
	# os-1 has vintner.bootstorage= and no vintner.recovery
	if [[ "$words" == *" vintner.bootstorage="* ]] && [[ "$words" != *" vintner.recovery"* ]]; then
		echo "os-1"
		return
	fi

	# Recovery: vintner.recovery=1 on UEFI. The BIOS slot.cfg carries
	# vintner.recovery_test=1 and no vintner.recovery=1, so BIOS recovery A is
	# vintner.recovery_test=1 after GRUB's "vintner: trying recovery-a".
	local rec=" vintner.recovery=1 "
	[ "$fw" = "bios" ] && rec=" vintner.recovery_test=1 "
	if [[ "$words" == *"$rec"* ]] && [[ "$words" != *" vintner.test_slot=b "* ]]; then
		if [ "$fw" = "bios" ] && ! clean_ansi < "$log" | grep -qE 'vintner: trying recovery-a($|[^-[:alnum:]])'; then
			echo "recovery-a-missing-bios-log"
			return
		fi
		echo "recovery-a"
		return
	fi

	if [[ "$words" == *"$rec"* ]] && [[ "$words" == *" vintner.test_slot=b "* ]]; then
		echo "recovery-b"
		return
	fi

	echo "unknown"
}

get_root_sha() {
	local disk="$1"
	python3 - "$disk" "$ROOT_START" "$ROOT_SECTORS" <<'EOF'
import sys, hashlib
disk_path, start_sec, num_sec = sys.argv[1], int(sys.argv[2]), int(sys.argv[3])
h = hashlib.sha256()
with open(disk_path, "rb") as f:
	f.seek(start_sec * 512)
	rem = num_sec * 512
	while rem > 0:
		chunk = f.read(min(rem, 4 * 1024 * 1024))
		if not chunk:
			break
		h.update(chunk)
		rem -= len(chunk)
print(h.hexdigest())
EOF
}

editenv() {
	"$ROOT/usr/lib/x86_64-linux-gnu/ld-linux-x86-64.so.2" --library-path "$ROOT/usr/lib/x86_64-linux-gnu" \
		"$ROOT/usr/bin/grub-editenv" "$@"
}

get_counter_info() {
	local disk="$1"
	local fw="$2"

	if [ "$fw" = "uefi" ]; then
		local os1_name reca_name
		os1_name=$(mdir -b -i "$disk@@$BS_OFFSET" ::/EFI/Linux 2>/dev/null | sed 's|.*/||' | grep -E '^vintner-os-1(\+[0-9]+-[0-9]+)?\.efi$' || true)
		reca_name=$(mdir -b -i "$disk@@$BS_OFFSET" ::/EFI/Linux 2>/dev/null | sed 's|.*/||' | grep -E '^vintner-recovery-a(\+[0-9]+-[0-9]+)?\.efi$' || true)
		local os1_tag reca_tag
		os1_tag=$(echo "$os1_name" | sed -n 's/^vintner-os-1\(\+[0-9]\+-[0-9]\+\)\.efi$/\1/p')
		reca_tag=$(echo "$reca_name" | sed -n 's/^vintner-recovery-a\(\+[0-9]\+-[0-9]\+\)\.efi$/\1/p')
		printf 'os1_name=%q os1_tag=%q reca_name=%q reca_tag=%q\n' "$os1_name" "$os1_tag" "$reca_name" "$reca_tag"
	else
		local tmp_env="$OUT/tmp_grubenv"
		mtype -i "$disk@@$BS_OFFSET" ::/grub/grubenv > "$tmp_env" 2>/dev/null || true
		local t1 booted tra vars
		vars=$(editenv "$tmp_env" list 2>/dev/null | grep -E '^VINTNER_' | sort | paste -sd ';' || true)
		t1=$(editenv "$tmp_env" list 2>/dev/null | sed -n 's/^VINTNER_T_1=//p')
		booted=$(editenv "$tmp_env" list 2>/dev/null | sed -n 's/^VINTNER_BOOTED=//p')
		tra=$(editenv "$tmp_env" list 2>/dev/null | sed -n 's/^VINTNER_T_RA=//p')
		rm -f "$tmp_env"
		printf 't1=%q booted=%q tra=%q vars=%q\n' "$t1" "$booted" "$tra" "$vars"
	fi
}

rm -f "$OUT"/d[1-6].tsv
touch "$OUT/d1.tsv" "$OUT/d2.tsv" "$OUT/d3.tsv" "$OUT/d4.tsv" "$OUT/d5.tsv" "$OUT/d6.tsv"

# --- 5. Cases Execution -----------------------------------------------------
for case_id in G0 F1 F2 F3 F4; do
	echo "=================================================================="
	echo "=== Running Case $case_id ($FIRMWARE) ==="
	echo "=================================================================="

	CASE_DISK="$OUT/${case_id}.raw"
	rm -f "$CASE_DISK"
	cp --sparse=always "$BASE_RAW" "$CASE_DISK"

	CASE_VARS=""
	if [ "$FIRMWARE" = "uefi" ]; then
		CASE_VARS="$OUT/${case_id}-vars.fd"
		# shellcheck disable=SC2153
		cp "$BASE_VARS" "$CASE_VARS"
		# Replace recovery UKIs on boot storage with test UKIs
		mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$TEST_RECA_UKI" ::/EFI/Linux/vintner-recovery-a+3-0.efi
		mcopy -o -i "$CASE_DISK@@$BS_OFFSET" "$TEST_RECB_UKI" ::/EFI/Linux/vintner-recovery-b.efi
	fi

	# Host modification per case
	case "$case_id" in
		G0)
			echo "Case G0: Untouched disk (control)"
			;;
		F1)
			echo "Case F1: Zero primary superblock (1024 bytes at offset 1024 of root partition)"
			python3 - "$CASE_DISK" "$ROOT_START" <<'PYEOF'
import sys
disk_path, root_start = sys.argv[1], int(sys.argv[2])
offset = root_start * 512 + 1024
with open(disk_path, "r+b") as f:
	f.seek(offset)
	f.write(b"\x00" * 1024)
PYEOF
			;;
		F2)
			echo "Case F2: Remove /usr/lib/systemd/systemd and /sbin/init"
			echo "Block devices on host before losetup (F2):"
			lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
			LOOP=$(sudo losetup -P -f --show "$CASE_DISK")
			sudo mount "${LOOP}p1" "$MNT"
			# shellcheck disable=SC2033
			sudo rm -f "$MNT/usr/lib/systemd/systemd" "$MNT/sbin/init"
			sudo umount "$MNT"
			sudo losetup -d "$LOOP"
			LOOP=""
			;;
		F3)
			echo "Case F3: Append failing mount to /etc/fstab"
			echo "Block devices on host before losetup (F3):"
			lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
			LOOP=$(sudo losetup -P -f --show "$CASE_DISK")
			sudo mount "${LOOP}p1" "$MNT"
			echo "/dev/disk/by-uuid/00000000-0000-0000-0000-000000000000 /mnt/missing ext4 defaults 0 2" | sudo tee -a "$MNT/etc/fstab" >/dev/null
			sudo umount "$MNT"
			sudo losetup -d "$LOOP"
			LOOP=""
			;;
		F4)
			echo "Case F4: Install and enable failing health check service"
			echo "Block devices on host before losetup (F4):"
			lsblk -o NAME,SIZE,MODEL,TRAN,MOUNTPOINTS
			LOOP=$(sudo losetup -P -f --show "$CASE_DISK")
			sudo mount "${LOOP}p1" "$MNT"
			cat <<'SVCEOF' | sudo tee "$MNT/usr/lib/systemd/system/vintner-test-health.service" >/dev/null
[Unit]
Before=boot-complete.target

[Service]
Type=oneshot
ExecStart=/bin/false

[Install]
RequiredBy=boot-complete.target
SVCEOF
			sudo chmod 0644 "$MNT/usr/lib/systemd/system/vintner-test-health.service"
			sudo mkdir -p "$MNT/etc/systemd/system/boot-complete.target.requires"
			sudo ln -sf /usr/lib/systemd/system/vintner-test-health.service "$MNT/etc/systemd/system/boot-complete.target.requires/vintner-test-health.service"
			sudo umount "$MNT"
			sudo losetup -d "$LOOP"
			LOOP=""
			;;
	esac

	# Run boots for this case
	BOOTS_RUN=0
	ROOT_SHA_BEFORE=""
	ROOT_SHA_AFTER=""
	# declare -A keeps an existing array's elements, so clear the previous case's.
	unset BOOT_IMG BOOT_TERM BOOT_FAIL_LINE BOOT_COUNTERS BOOT_BLESS
	declare -A BOOT_IMG
	declare -A BOOT_TERM
	declare -A BOOT_FAIL_LINE
	declare -A BOOT_COUNTERS
	declare -A BOOT_BLESS

	for boot in 1 2 3 4 5; do
		BOOTS_RUN=$boot
		BOOT_LOG="$OUT/${case_id}-boot${boot}.log"

		if [ "$case_id" != "G0" ]; then
			CURRENT_ROOT_SHA=$(get_root_sha "$CASE_DISK")
			ROOT_SHA_BEFORE="$CURRENT_ROOT_SHA"
		fi

		echo "--- Booting $case_id Boot $boot ---"
		START_TIME=$(date +%s)
		run_qemu "$CASE_DISK" "$SEED_IMG" "$BOOT_LOG" 120 "$CASE_VARS"
		END_TIME=$(date +%s)
		DURATION=$((END_TIME - START_TIME))

		# run_qemu does not return timeout's status; a run killed at 120 s spans
		# at least 120 whole seconds of date +%s.
		if [ "$DURATION" -ge 120 ]; then
			BOOT_TERM[$boot]="killed at timeout"
		else
			BOOT_TERM[$boot]="powered off"
		fi

		IMG=$(get_booted_image "$BOOT_LOG" "$FIRMWARE")
		BOOT_IMG[$boot]="$IMG"

		COUNTERS=$(get_counter_info "$CASE_DISK" "$FIRMWARE")
		BOOT_COUNTERS[$boot]="$COUNTERS"

		BLESS_LINE=$(clean_ansi < "$BOOT_LOG" | grep -m1 '^BLESS_ACTIVE: ' || true)
		BOOT_BLESS[$boot]="$BLESS_LINE"

		case "$case_id" in
			F1)
				BOOT_FAIL_LINE[$boot]=$(clean_ansi < "$BOOT_LOG" | grep -m1 -E 'Gave up waiting for root file system device|mount: mounting .* on /root failed|ALERT! .* does not exist' || true)
				;;
			F2)
				f2_init=$(clean_ansi < "$BOOT_LOG" | grep -m1 -E 'Target filesystem doesn'\''t have requested|No working init found' || true)
				f2_panic=$(clean_ansi < "$BOOT_LOG" | grep -m1 'Kernel panic - not syncing' || true)
				BOOT_FAIL_LINE[$boot]="${f2_init:+$f2_init; kernel panic: ${f2_panic:-none}}"
				;;
			F3)
				BOOT_FAIL_LINE[$boot]=$(clean_ansi < "$BOOT_LOG" | grep -m1 'You are in emergency mode' || true)
				;;
			F4)
				h_fail=$(clean_ansi < "$BOOT_LOG" | grep -m1 'Failed to start vintner-test-health\.service' || true)
				t_fail=$(clean_ansi < "$BOOT_LOG" | grep -m1 'Dependency failed for boot-complete\.target' || true)
				BOOT_FAIL_LINE[$boot]=""
				[ -n "$h_fail" ] && [ -n "$t_fail" ] && BOOT_FAIL_LINE[$boot]="$h_fail; $t_fail"
				;;
		esac

		echo "Boot $boot: img=$IMG, term=${BOOT_TERM[$boot]} (${DURATION}s), counters: $COUNTERS"

		if [ "$IMG" != "os-1" ]; then
			ROOT_SHA_AFTER=$(get_root_sha "$CASE_DISK")
			break
		fi
	done

	# Evaluate checks for this case
	if [ "$case_id" = "G0" ]; then
		D1_OK=true
		D1_EVID_PARTS=()

		if [ "$BOOTS_RUN" -ne 5 ]; then
			D1_OK=false
			D1_EVID_PARTS+=("ran $BOOTS_RUN boots, expected 5")
		fi

		for b in 1 2 3 4 5; do
			if [ "${BOOT_IMG[$b]}" != "os-1" ]; then
				D1_OK=false
				D1_EVID_PARTS+=("boot $b ran ${BOOT_IMG[$b]}")
			fi
			if [ "${BOOT_BLESS[$b]}" != "BLESS_ACTIVE: active" ]; then
				D1_OK=false
				D1_EVID_PARTS+=("boot $b bless status '${BOOT_BLESS[$b]}'")
			fi
			eval "${BOOT_COUNTERS[$b]}"
			if [ "$FIRMWARE" = "uefi" ]; then
				if [ "$os1_tag" != "+3-0" ]; then
					D1_OK=false
					D1_EVID_PARTS+=("boot $b counter $os1_tag")
				fi
			else
				if [ "$t1" != "xxx" ] || [ -n "$booted" ]; then
					D1_OK=false
					D1_EVID_PARTS+=("boot $b vars $vars")
				fi
			fi
		done

		if [ "$D1_OK" = true ]; then
			D1_STATUS="PASS"
			if [ "$FIRMWARE" = "uefi" ]; then
				D1_EVID="5 boots: os-1, blessed (vintner-bless.service active); counter re-armed each time (UEFI +3-0); recovery never booted"
			else
				D1_EVID="5 boots: os-1, blessed (vintner-bless.service active); counter re-armed each time (VINTNER_T_1=xxx, no VINTNER_BOOTED); recovery never booted"
			fi
		else
			D1_STATUS="FAIL"
			D1_EVID=$(IFS="; "; echo "${D1_EVID_PARTS[*]}")
		fi
		printf "D1\tG0\t%s\t%s\n" "$D1_STATUS" "$D1_EVID" >> "$OUT/d1.tsv"
	else
		# Check D2
		D2_OK=true
		D2_EVID_PARTS=()
		for b in 1 2 3; do
			if [ "${BOOT_IMG[$b]:-}" != "os-1" ]; then
				D2_OK=false
				D2_EVID_PARTS+=("boot $b ran ${BOOT_IMG[$b]:-none}")
			fi
		done

		if [ "$FIRMWARE" = "uefi" ]; then
			eval "${BOOT_COUNTERS[1]}"
			b1_tag="$os1_tag"
			eval "${BOOT_COUNTERS[2]}"
			b2_tag="$os1_tag"
			eval "${BOOT_COUNTERS[3]}"
			b3_tag="$os1_tag"

			if [ "$b1_tag" != "+2-1" ] || [ "$b2_tag" != "+1-2" ] || [ "$b3_tag" != "+0-3" ]; then
				D2_OK=false
				D2_EVID_PARTS+=("counters: B1=$b1_tag B2=$b2_tag B3=$b3_tag, expected +2-1, +1-2, +0-3")
			fi
			counter_seq="B1: $b1_tag, B2: $b2_tag, B3: $b3_tag"
		else
			eval "${BOOT_COUNTERS[1]}"
			b1_t1="$t1" b1_booted="$booted"
			eval "${BOOT_COUNTERS[2]}"
			b2_t1="$t1" b2_booted="$booted"
			eval "${BOOT_COUNTERS[3]}"
			b3_t1="$t1" b3_booted="$booted"

			if [ "$b1_t1" != "xx" ] || [ "$b1_booted" != "1" ] || \
			   [ "$b2_t1" != "x" ] || [ "$b2_booted" != "1" ] || \
			   [ "$b3_t1" != "0" ] || [ "$b3_booted" != "1" ]; then
				D2_OK=false
				D2_EVID_PARTS+=("counters: B1=T_1:${b1_t1}/B:${b1_booted} B2=T_1:${b2_t1}/B:${b2_booted} B3=T_1:${b3_t1}/B:${b3_booted}")
			fi
			counter_seq="B1: T_1=${b1_t1},BOOTED=${b1_booted}; B2: T_1=${b2_t1},BOOTED=${b2_booted}; B3: T_1=${b3_t1},BOOTED=${b3_booted}"
		fi

		fail_line="${BOOT_FAIL_LINE[1]:-}"
		for b in 1 2 3; do
			if [ -z "${BOOT_FAIL_LINE[$b]:-}" ]; then
				D2_OK=false
				D2_EVID_PARTS+=("boot $b: no failure evidence")
			fi
		done

		if [ "$D2_OK" = true ]; then
			D2_STATUS="PASS"
			D2_EVID="os-1 boots 1-3; counters: $counter_seq; failure: $fail_line"
		else
			D2_STATUS="FAIL"
			D2_EVID=$(IFS="; "; echo "${D2_EVID_PARTS[*]}; failure: $fail_line")
		fi
		printf "D2\t%s\t%s\t%s\n" "$case_id" "$D2_STATUS" "$D2_EVID" >> "$OUT/d2.tsv"

		# Check D3
		D3_OK=true
		D3_EVID_PARTS=()
		if [ "${BOOT_IMG[4]:-}" != "recovery-a" ]; then
			D3_OK=false
			D3_EVID_PARTS+=("boot 4 ran ${BOOT_IMG[4]:-none}, expected recovery-a")
		fi

		eval "${BOOT_COUNTERS[4]}"
		if [ "$FIRMWARE" = "uefi" ]; then
			if [ "$reca_tag" != "+2-1" ]; then
				D3_OK=false
				D3_EVID_PARTS+=("recovery A counter $reca_tag, expected +2-1")
			fi
			reca_evid="counter after: $reca_name"
		else
			if [ "$tra" != "xx" ] || [ "$booted" != "RA" ]; then
				D3_OK=false
				D3_EVID_PARTS+=("vars: T_RA=$tra, BOOTED=$booted, expected xx and RA")
			fi
			reca_evid="grubenv after: VINTNER_T_RA=$tra, VINTNER_BOOTED=$booted"
		fi

		if [ "$D3_OK" = true ]; then
			D3_STATUS="PASS"
			D3_EVID="boot 4 ran Recovery A (not B); $reca_evid"
		else
			D3_STATUS="FAIL"
			D3_EVID=$(IFS="; "; echo "${D3_EVID_PARTS[*]}")
		fi
		printf "D3\t%s\t%s\t%s\n" "$case_id" "$D3_STATUS" "$D3_EVID" >> "$OUT/d3.tsv"

		# Check D4
		B4_LOG="$OUT/${case_id}-boot4.log"
		REC_TEST_LINE=$(clean_ansi < "$B4_LOG" | grep -m1 '^VINTNER-RECOVERY-TEST ' || true)
		REC_RESULT=$(echo "$REC_TEST_LINE" | cut -d' ' -f2,3)
		D4_EVID="${REC_TEST_LINE:-none}"
		D4_MANIFEST_OK=true
		if [ "$FIRMWARE" = "uefi" ]; then
			# The test UKIs carry no vintner.root=, so recovery must name the base disk's root from the manifest.
			MANIFEST_LINE="root from the manifest on boot storage: UUID=$ROOT_FS_UUID"
			# Anchored at the end so the whole UUID is compared.
			if clean_ansi < "$B4_LOG" | grep -q "$MANIFEST_LINE\$"; then
				D4_EVID="$MANIFEST_LINE; $D4_EVID"
			else
				D4_MANIFEST_OK=false
				D4_EVID="no \"$MANIFEST_LINE\"; $D4_EVID"
			fi
		fi
		if [ "$case_id" = "F1" ] && [ "$D4_MANIFEST_OK" = true ]; then
			D4_STATUS="OBSERVED"
		elif [ "$case_id" != "F1" ] && [ "$D4_MANIFEST_OK" = true ] && [ "$REC_RESULT" = "root=mounted ro=yes" ]; then
			D4_STATUS="PASS"
		else
			D4_STATUS="FAIL"
		fi
		printf "D4\t%s\t%s\t%s\n" "$case_id" "$D4_STATUS" "$D4_EVID" >> "$OUT/d4.tsv"

		# Check D5
		if [ -n "$ROOT_SHA_BEFORE" ] && [ "$ROOT_SHA_BEFORE" = "$ROOT_SHA_AFTER" ]; then
			D5_STATUS="PASS"
			D5_EVID="unchanged (sha256=$ROOT_SHA_BEFORE)"
		else
			D5_STATUS="FAIL"
			D5_EVID="before=${ROOT_SHA_BEFORE:-none} after=${ROOT_SHA_AFTER:-none}"
		fi
		printf "D5\t%s\t%s\t%s\n" "$case_id" "$D5_STATUS" "$D5_EVID" >> "$OUT/d5.tsv"

		# Check D6
		D6_STATUS="OBSERVED"
		D6_EVID="B1: ${BOOT_TERM[1]:-none}; B2: ${BOOT_TERM[2]:-none}; B3: ${BOOT_TERM[3]:-none}"
		printf "D6\t%s\t%s\t%s\n" "$case_id" "$D6_STATUS" "$D6_EVID" >> "$OUT/d6.tsv"
	fi

	rm -f "$CASE_DISK"
	if [ "$FIRMWARE" = "uefi" ]; then
		rm -f "$CASE_VARS"
	fi
done

# --- 6. Summary and Verification --------------------------------------------
printf "ID\tCase\tStatus\tEvidence\n" > "$RESULTS"
cat "$OUT/d1.tsv" "$OUT/d2.tsv" "$OUT/d3.tsv" "$OUT/d4.tsv" "$OUT/d5.tsv" "$OUT/d6.tsv" >> "$RESULTS"
rm -f "$OUT"/d[1-6].tsv

echo "=================================================================="
echo "=== Test Summary Table ($FIRMWARE) ==="
echo "=================================================================="
cat "$RESULTS"
echo "=== versions.txt ==="
cat "$VERSIONS"

FAILURES=$(awk -F'\t' '$3=="FAIL"{print $1 "-" $2}' "$RESULTS")
if [ -n "$FAILURES" ]; then
	echo "ERROR: Fallback test failed on checks: $FAILURES" >&2
	exit 1
fi

echo "All $FIRMWARE recovery fallback checks passed successfully!"
