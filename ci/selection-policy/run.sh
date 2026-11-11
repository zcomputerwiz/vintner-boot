#!/bin/bash
# Proves on Debian 13's own systemd-boot binary how vintner UKI entries are
# ordered, counted and selected (architecture §4.1 and §10, ADR-0003).
# Needs: curl, xz, dpkg-deb, cpio, gzip, sfdisk, mtools, python3 with pefile,
# python3-virt-firmware, sbsigntool, openssl, qemu-system-x86_64 and the OVMF 4M
# firmware, including its Secure Boot build. No root.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=ci/out/selection-policy
ROOT=$OUT/root
MIRROR=https://deb.debian.org/debian
CODE=/usr/share/OVMF/OVMF_CODE_4M.fd
VARS=/usr/share/OVMF/OVMF_VARS_4M.fd
LOADER_GUID=4a67b082-0a4c-41cf-b6c7-440b29bb8c4f
export MTOOLS_SKIP_CHECK=1
mkdir -p "$OUT/debs" "$ROOT"
: > "$OUT/versions.txt"

# --- Debian trixie packages, unpacked without apt or root -------------------
[ -f "$OUT/Packages" ] || curl -sSfL "$MIRROR/dists/trixie/main/binary-amd64/Packages.xz" | xz -dc > "$OUT/Packages"
field() { # package field
	awk -v p="$1" -v f="$2:" '$1=="Package:"{cur=$2} cur==p && $1==f{sub(/^[^ ]+ /,""); print; exit}' "$OUT/Packages"
}
fetch() {
	local fn
	fn=$(field "$1" Filename)
	[ -n "$fn" ] || { echo "no $1 in trixie" >&2; exit 1; }
	[ -f "$OUT/debs/${fn##*/}" ] || curl -sSfL "$MIRROR/$fn" -o "$OUT/debs/${fn##*/}"
	dpkg-deb -x "$OUT/debs/${fn##*/}" "$ROOT"
	echo "$1 $(field "$1" Version)" >> "$OUT/versions.txt"
}
kernel_pkg=$(field linux-image-amd64 Depends | sed 's/ .*//; s/,$//')
for p in systemd-boot-efi systemd-ukify busybox-static "$kernel_pkg"; do fetch "$p"; done
KERNEL=$(ls "$ROOT"/boot/vmlinuz-*)
STUB=$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub
SDBOOT=$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi
first() { local f; for f; do [ -e "$f" ] && { echo "$f"; return; }; done; return 1; }
UKIFY=$(first "$ROOT"/usr/lib/systemd/ukify "$ROOT"/usr/bin/ukify)
BUSYBOX=$(first "$ROOT"/usr/bin/busybox "$ROOT"/bin/busybox)

# --- test initramfs: print the test id, then power off or reboot -----------
rm -rf "$OUT/initrd" && mkdir -p "$OUT/initrd"/{bin,proc,dev}
cp "$BUSYBOX" "$OUT/initrd/bin/busybox"
cat > "$OUT/initrd/init" <<'EOF'
#!/bin/busybox sh
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t devtmpfs dev /dev
arg() { /bin/busybox sed -n "s/.*vintner.$1=\([^ ]*\).*/\1/p" /proc/cmdline; }
echo "VINTNER-TEST-BOOTED id=$(arg test_id)" > /dev/ttyS0
if [ "$(arg test_action)" = reboot ]; then /bin/busybox reboot -f; fi
/bin/busybox poweroff -f
EOF
chmod +x "$OUT/initrd/init"
(cd "$OUT/initrd" && find . | cpio -o -H newc --owner=0:0 --quiet | gzip -9) > "$OUT/initrd.img"

# --- UKIs, with .osrel per ADR-0003 ----------------------------------------
uki() { # name test_id action image_id image_version [menu title, default: the name]
	local osrel=$OUT/osrel-$1
	if [ -n "$4" ]; then
		printf 'ID=debian\nIMAGE_ID=%s\nIMAGE_VERSION=%s\nPRETTY_NAME="%s"\n' "$4" "$5" "${6:-$1}" > "$osrel"
	else
		printf 'ID=debian\nVERSION_ID=13\nPRETTY_NAME="Foreign Debian"\n' > "$osrel"
	fi
	python3 "$UKIFY" build --linux="$KERNEL" --initrd="$OUT/initrd.img" \
		--stub="$STUB" --os-release="@$osrel" --output="$OUT/uki-$1.efi" \
		--cmdline="console=ttyS0 panic=-1 vintner.test_id=$2 vintner.test_action=$3" >/dev/null
}
uki os-1 os-1 poweroff vintner-os 1
uki os-1-fail os-1 reboot vintner-os 1
uki os-2 os-2 poweroff vintner-os 2
uki os-2-fail os-2 reboot vintner-os 2
uki ra ra poweroff vintner-recovery-a 20261015
uki rb rb poweroff vintner-recovery-b 20261015
uki foreign foreign poweroff "" ""
# WO-0010 4.3: variants. No counter; the sort key is the ordinary IMAGE_ID and the
# version is <gen>~<id>, so a variant sorts directly after its generation.
uki var-os-2 os-2-safe poweroff vintner-os "2~safe"
uki var-os-1 os-1-safe poweroff vintner-os "1~safe"
# WO-0010 4.1: the titles the publisher and the installer write.
uki menu-os-2 os-2 poweroff vintner-os 2 "Test OS (2026-01-02)"
uki menu-var-2-safe os-2-safe poweroff vintner-os "2~safe" "Test OS (2026-01-02): Safe Mode"
uki menu-var-2-netw os-2-netw poweroff vintner-os "2~netw" "Test OS (2026-01-02): Safe Mode with Networking"
uki menu-os-1 os-1 poweroff vintner-os 1 "Test OS (2026-01-01)"
uki menu-var-1-safe os-1-safe poweroff vintner-os "1~safe" "Test OS (2026-01-01): Safe Mode"
uki menu-ra ra poweroff vintner-recovery-a 20261015 "Recovery Console"
uki menu-rb rb poweroff vintner-recovery-b 20261015 "Recovery Console (backup)"

# --- disk: GPT with ESP (loader) and XBOOTLDR (UKIs) -----------------------
IMG=$OUT/disk.img
new_disk() { # "uki-src:name" ... (names land in /EFI/Linux on XBOOTLDR)
	rm -f "$IMG" && truncate -s 400M "$IMG"
	printf 'label: gpt\nsize=64MiB, type=C12A7328-F81F-11D2-BA4B-00A0C93EC93B\ntype=BC13C2FF-59E6-4262-A352-B275FD6F7172\n' | sfdisk -q "$IMG"
	ESP="$IMG@@$(( $(sfdisk -J "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][0]["start"])') * 512 ))"
	XBL="$IMG@@$(( $(sfdisk -J "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][1]["start"])') * 512 ))"
	mformat -i "$ESP" -F -v ESP ::
	mformat -i "$XBL" -F -v XBOOTLDR ::
	mmd -i "$ESP" ::/EFI ::/EFI/BOOT ::/EFI/vintner ::/EFI/Linux ::/loader ::/loader/entries
	mmd -i "$XBL" ::/EFI ::/EFI/Linux
	mcopy -i "$ESP" "$SDBOOT" ::/EFI/BOOT/BOOTX64.EFI
	mcopy -i "$ESP" "$SDBOOT" ::/EFI/vintner/systemd-bootx64.efi
	mcopy -i "$ESP" firmware/uefi/loader.conf ::/loader/loader.conf
	local e
	for e in "$@"; do mcopy -i "$XBL" "$OUT/uki-${e%%:*}.efi" "::/EFI/Linux/${e#*:}"; done
}
listing() { mdir -b -i "$XBL" ::/EFI/Linux | sed 's|.*/||' | sort | tr '\n' ' '; }

new_vars() { # [name value]...: LoaderInfo string variables (UTF-16LE, NUL-terminated)
	cp "$VARS" "$OUT/vars.fd" && chmod u+w "$OUT/vars.fd"
	[ $# -gt 0 ] || return 0
	python3 - "$LOADER_GUID" "$@" > "$OUT/vars.json" <<'EOF'
import json, sys
guid, kv = sys.argv[1], sys.argv[2:]
print(json.dumps({"version": 2, "variables": [
    {"name": kv[i], "guid": guid, "attr": 7, "data": (kv[i + 1] + "\0").encode("utf-16-le").hex()}
    for i in range(0, len(kv), 2)]}))
EOF
	virt-fw-vars -i "$OUT/vars.fd" -o "$OUT/vars.fd" --set-json "$OUT/vars.json" >/dev/null
}

ACCEL=tcg
[ -w /dev/kvm ] && ACCEL=kvm
BOOT_TIMEOUT=300
QEMU_SB=()
boot() { # one boot; prints the test id that booted, or NONE. With QMP=1, QMP commands come from stdin.
	local log=$OUT/$SCEN-boot$1.log in=/dev/null qmp=()
	[ "${QMP:-}" = 1 ] && in=/dev/stdin qmp=(-qmp stdio)
	timeout "$BOOT_TIMEOUT" qemu-system-x86_64 -machine "q35,accel=$ACCEL,smm=on" -m 512 -nodefaults -display none \
		-monitor none -serial "file:$log" -no-reboot "${QEMU_SB[@]}" "${qmp[@]}" \
		-drive "if=pflash,format=raw,unit=0,file=$CODE,readonly=on" \
		-drive "if=pflash,format=raw,unit=1,file=$OUT/vars.fd" \
		-drive "file=$IMG,format=raw,if=virtio" <"$in" >/dev/null 2>&1 || true
	grep -ao -m1 'VINTNER-TEST-BOOTED id=[A-Za-z0-9-]*' "$log" | sed 's/.*id=//' || echo NONE
}

fail=0
SUMMARY=$OUT/summary.tsv
printf 'scenario\tbooted\tfinal /EFI/Linux on XBOOTLDR\tresult\n' > "$SUMMARY"
scenario() { # name expected-ids(space separated) [OBSERVE]
	SCEN=$1
	local got="" i=1
	for _ in $2; do got="$got $(boot $i)"; i=$((i + 1)); done
	got=${got# }
	local result=PASS
	if [ "${3:-}" = OBSERVE ]; then result=OBSERVED
	elif [ "$got" != "$2" ]; then result=FAIL; fail=1; fi
	printf '%s\t%s\t%s\t%s\n' "$SCEN" "$got" "$(listing)" "$result" | tee -a "$SUMMARY"
}
S1=(os-2:vintner-os-2+3.efi os-1:vintner-os-1.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi)

new_disk "${S1[@]}"; new_vars
scenario S1-order "os-2"
new_disk os-2-fail:vintner-os-2+1-2.efi os-1:vintner-os-1.efi; new_vars
scenario S2-failing-candidate "os-2 os-1"
new_disk os-2:vintner-os-2+0-3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi; new_vars
scenario S3-all-ordinary-bad "ra"
new_disk os-2:vintner-os-2+0-3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+0-3.efi rb:vintner-recovery-b.efi; new_vars
scenario S4-recovery-a-bad "rb"
new_disk "${S1[@]}" foreign:debian-6.12.efi; new_vars
mcopy -i "$ESP" "$OUT/uki-foreign.efi" ::/EFI/Linux/debian-6.12.efi
printf 'title Foreign\nefi /EFI/Linux/debian-6.12.efi\n' > "$OUT/aaa.conf" && mcopy -i "$ESP" "$OUT/aaa.conf" ::/loader/entries/aaa.conf
scenario S5-foreign-entries "os-2"
new_disk os-1-fail:vintner-os-1+3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi; new_vars
scenario S6-good-image-fails-repeatedly "os-1 os-1 os-1 ra"
new_disk "${S1[@]}" foreign:debian-6.12.efi; new_vars LoaderEntryDefault debian-6.12.efi
scenario S7-LoaderEntryDefault-overrides "foreign"
new_disk "${S1[@]}"; new_vars LoaderEntryOneShot 'vintner-recovery-*'
scenario S8-one-shot-recovery "ra os-2"
new_disk os-2:vintner-os-2+3.efi os-1:vintner-os-1.efi ra:vintner-recovery-a+0-3.efi rb:vintner-recovery-b.efi; new_vars LoaderEntryOneShot 'vintner-recovery-*'
scenario S9-one-shot-recovery-a-bad "rb"
head -c 4096 "$OUT/uki-os-2.efi" > "$OUT/uki-trunc.efi"
new_disk trunc:vintner-os-2+3.efi os-1:vintner-os-1.efi; new_vars
scenario S10-image-fails-to-load "x" OBSERVE

# Hidden menu (timeout 0): space pressed through the first 15 s, then Enter.
key() { # qcode
	printf '{"execute":"input-send-event","arguments":{"events":[{"type":"key","data":{"down":%s,"key":{"type":"qcode","data":"%s"}}}]}}\n' true "$1" false "$1"
}
keys() {
	echo '{"execute":"qmp_capabilities"}'
	for _ in $(seq 60); do key spc; sleep 0.25; done
	sleep 5; key ret; sleep 120
}
new_disk "${S1[@]}"; new_vars
SCEN=S13-key-shows-menu
got=$(QMP=1 boot 1 < <(keys))
grep -aq 'os-1' "$OUT/$SCEN-boot1.log" && menu=shown || menu="not shown"
printf '%s (menu %s)\t%s\t%s\tOBSERVED\n' "$SCEN" "$menu" "$got" "$(listing)" | tee -a "$SUMMARY"

# WO-0010 4.8: F8 opens the menu on UEFI, as the documented menu key (docs/menu-key.md).
# systemd-boot shows it for any key pressed at start while "timeout 0" keeps it hidden otherwise.
# A boot takes a few seconds and a key press that lands before or after the loader reads
# the keyboard is lost (S13 sees the same), so F8 is pressed again and again
# and up to three boots are tried. Each boot that shows the menu proves the key; none does
# when no key is sent (checked by removing the presses).
f8_keys() {
	echo '{"execute":"qmp_capabilities"}'
	for _ in $(seq 60); do key f8; sleep 0.25; done
	sleep 5; key ret; sleep 120
}
SCEN=S21-f8-shows-menu
result=FAIL got=-
for _ in 1 2 3; do
	new_disk "${S1[@]}"; new_vars
	got=$(QMP=1 boot 1 < <(f8_keys))
	if grep -aq 'os-1' "$OUT/$SCEN-boot1.log"; then result=PASS; break; fi
done
[ "$result" = PASS ] || { fail=1; echo "$SCEN: F8 did not open the menu in three boots" >&2; }
printf '%s\t%s\t%s\t%s\n' "$SCEN" "$got" "$(listing)" "$result" | tee -a "$SUMMARY"

# WO-0010 4.7: "vintner menu-timeout <n>" changes the timeout line of loader.conf (the
# file the Go test checks against firmware/uefi/loader.conf). With "timeout 4" the menu is
# shown without a key and the default entry boots when it runs out; with "timeout 0" no key
# means no menu (S1 boots the same disk without one).
set_timeout() { sed "s/^timeout 0\$/timeout $1/" firmware/uefi/loader.conf > "$OUT/loader.conf"; mcopy -o -i "$ESP" "$OUT/loader.conf" ::/loader/loader.conf; }
new_disk "${S1[@]}"; new_vars
set_timeout 4
SCEN=S22-menu-timeout-shows-menu
got=$(boot 1)
result=PASS
grep -aq 'os-1' "$OUT/$SCEN-boot1.log" || { result=FAIL; fail=1; echo "$SCEN: no menu with timeout 4" >&2; }
[ "$got" = os-2 ] || { result=FAIL; fail=1; echo "$SCEN: booted '$got', expected the default os-2 after the timeout" >&2; }
printf '%s\t%s\t%s\t%s\n' "$SCEN" "$got" "$(listing)" "$result" | tee -a "$SUMMARY"
new_disk "${S1[@]}"; new_vars
SCEN=S23-no-timeout-no-menu
got=$(boot 1)
result=PASS
if grep -aq 'os-1' "$OUT/$SCEN-boot1.log"; then result=FAIL; fail=1; echo "$SCEN: a menu with timeout 0 and no key" >&2; fi
printf '%s\t%s\t%s\t%s\n' "$SCEN" "$got" "$(listing)" "$result" | tee -a "$SUMMARY"

# WO-0010 4.3 (D-C): a variant has no counter and a name outside "default vintner-*",
# so the default never selects it, and with every ordinary image bad recovery A
# still boots without input. An uncounted variant of a broken kernel would
# otherwise boot for ever.
V=(var-os-2:vintner_variant-os-2-safe.efi var-os-1:vintner_variant-os-1-safe.efi)
new_disk "${S1[@]}" "${V[@]}"; new_vars
scenario S14-variants-are-never-the-default "os-2"
new_disk os-2:vintner-os-2+0-3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi "${V[@]}"; new_vars
scenario S15-variants-of-bad-images-do-not-beat-recovery "ra"
new_disk os-2:vintner-os-2+0-3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+0-3.efi rb:vintner-recovery-b.efi "${V[@]}"; new_vars
scenario S16-variants-and-no-recovery-a "rb"

# WO-0010 4.4: "vintner next-boot generation <n>" sets LoaderEntryOneShot to "vintner-os-<n>.efi".
# It selects that generation over a newer one, and a bad one too (finding 4: the first match
# wins, whatever its state). The next boot is the default order again.
new_disk os-2:vintner-os-2+3.efi os-1:vintner-os-1+3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi; new_vars LoaderEntryOneShot vintner-os-1.efi
scenario S19-one-shot-generation "os-1 os-2"
new_disk os-2:vintner-os-2+3.efi os-1:vintner-os-1+0-3.efi ra:vintner-recovery-a+3.efi rb:vintner-recovery-b.efi; new_vars LoaderEntryOneShot vintner-os-1.efi
scenario S20-one-shot-bad-generation "os-1 os-2"

# Booting a variant (here chosen with LoaderEntryOneShot, as 4.4's next-boot variant will)
# renames nothing: every file name, counter included, is the same afterwards (D-D).
new_disk "${S1[@]}" "${V[@]}"; new_vars LoaderEntryOneShot vintner_variant-os-2-safe.efi
before=$(listing)
scenario S18-variant-boot-renames-nothing "os-2-safe"
if [ "$(listing)" != "$before" ]; then
	echo "S18: the variant boot changed the file names: '$before' became '$(listing)'" >&2
	printf 'S18-variant-boot-renames-nothing\t-\t%s\tFAIL\n' "$(listing)" | tee -a "$SUMMARY"
	fail=1
fi

# The menu lists each variant directly after its generation (version "<gen>~<id>"),
# the variants of one generation among themselves in descending order, and every
# older generation after them. LoaderConfigTimeoutOneShot shows the menu without a key (a key press during a fast boot is a matter of luck).
new_disk menu-os-2:vintner-os-2+3.efi menu-var-2-safe:vintner_variant-os-2-safe.efi menu-var-2-netw:vintner_variant-os-2-netw.efi \
	menu-os-1:vintner-os-1.efi menu-var-1-safe:vintner_variant-os-1-safe.efi menu-ra:vintner-recovery-a+3.efi menu-rb:vintner-recovery-b.efi
new_vars LoaderConfigTimeoutOneShot 30 # the menu for 30 s, with no key to catch
SCEN=S17-variant-menu-order
QMP=1 boot 1 < <(echo "{\"execute\":\"qmp_capabilities\"}"; sleep 10; key ret; sleep 120) > /dev/null
order=$(sed 's/\x1b\[[0-9;]*[A-Za-z]//g' "$OUT/$SCEN-boot1.log" | tr -d '\r' | { grep -aoE 'Test OS \(2026-01-0[12]\)(: Safe Mode( with Networking)?)?|Recovery Console( \(backup\))?' || true; } | awk '!seen[$0]++' | paste -sd ',')
want="Test OS (2026-01-02),Test OS (2026-01-02): Safe Mode,Test OS (2026-01-02): Safe Mode with Networking,Test OS (2026-01-01),Test OS (2026-01-01): Safe Mode,Recovery Console,Recovery Console (backup)"
result=PASS; [ "$order" = "$want" ] || { result=FAIL; fail=1; echo "$SCEN: menu order '$order', expected '$want'" >&2; }
printf '%s\t%s\t%s\t%s\n' "$SCEN" "$order" "$(listing)" "$result" | tee -a "$SUMMARY"

# --- Secure Boot: a listed entry that fails LoadImage (ADR-0003 finding 5) ---
# Throwaway test key, made per run: one self-signed certificate is PK, KEK and
# db. systemd-boot and every UKI that should load are signed with it.
SB=$OUT/sb
mkdir -p "$SB"
openssl req -new -x509 -newkey rsa:2048 -sha256 -days 30 -nodes -subj "/CN=vintner selection-policy test key" \
	-keyout "$SB/test.key" -out "$SB/test.crt" 2>/dev/null
G=bbbbbbbb-cccc-dddd-eeee-ffffffffffff
virt-fw-vars -i "$VARS" -o "$SB/vars.fd" --set-pk "$G" "$SB/test.crt" --add-kek "$G" "$SB/test.crt" \
	--add-db "$G" "$SB/test.crt" --secure-boot >/dev/null
sign() { sbsign --key "$SB/test.key" --cert "$SB/test.crt" --output "$2" "$1" >/dev/null 2>&1; }
sign "$SDBOOT" "$SB/systemd-bootx64.efi"
for u in os-1 os-2; do sign "$OUT/uki-$u.efi" "$OUT/uki-$u-signed.efi"; done
SDBOOT=$SB/systemd-bootx64.efi
CODE=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd
VARS=$SB/vars.fd
QEMU_SB=(-global "driver=cfi.pflash01,property=secure,value=on")
new_disk os-2-signed:vintner-os-2+3.efi os-1-signed:vintner-os-1.efi; new_vars
scenario S11-secure-boot-signed "os-2"
# The unsigned os-2 never runs. OVMF falls back to \EFI\BOOT\BOOTX64.EFI, which is
# systemd-boot again, so each power-on takes two tries, then the first stops
# at "No bootable option". The second power-on takes the last try and the
# fallback run boots os-1.
BOOT_TIMEOUT=90
new_disk os-2:vintner-os-2+3.efi os-1-signed:vintner-os-1.efi; new_vars
scenario S12-secure-boot-unsigned-entry "NONE os-1"

echo "trixie packages:"; cat "$OUT/versions.txt"
exit $fail
