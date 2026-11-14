#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Secure Boot tier B, installed (WO-0010 5.2, docs/secure-boot.md §8): the guest
# is installed by `vintner install --secure-boot B`, writes its own request for
# MokManager from the running system, a person (drive.py) confirms it at the
# console, and the next boot runs with Secure Boot enforced through Debian's
# shim, the machine-signed systemd-boot and the machine-signed UKI, with no
# health report. ci/mok-chain proves the chain on hand-made disks; this row
# proves what vintner itself writes. See README.md for the rows.
#
# Requires: root via sudo, KVM, loop devices with partition scanning, mtools,
# qemu-system-x86, qemu-utils, cloud-image-utils, sfdisk, python3,
# python3-virt-firmware, sbsigntool, openssl, golang-go, and the OVMF 4M
# firmware with its Secure Boot build.
set -euo pipefail
cd "$(dirname "$0")/../.."

FIRMWARE=uefi
OUT=ci/out/secure-boot-b
RESULTS_B=$OUT/results-b.tsv
SECBOOT_CODE=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd
VARS_TEMPLATE=/usr/share/OVMF/OVMF_VARS_4M.fd
OWNER=bbbbbbbb-cccc-dddd-eeee-ffffffffffff
SHIM_GUID=605dab50-e046-4300-abb6-3dd810dd8b23
PASSWORD=vintner1
export MTOOLS_SKIP_CHECK=1
mkdir -p "$OUT"
[ -f "$SECBOOT_CODE" ] || { echo "no $SECBOOT_CODE (package ovmf)" >&2; exit 1; }

# shellcheck disable=SC2317 # run by the EXIT trap
dump_logs() {
	local rc=$?
	if [ "$rc" != 0 ]; then
		for l in "$OUT"/T*.log; do
			[ -f "$l" ] && { echo "--- last 60 lines of $l ---"; tr -d '\r' < "$l" | tail -n 60; }
		done
	fi
}
trap dump_logs EXIT

# --- 1. The installed base ----------------------------------------------------
# `vintner install --secure-boot B` in a chroot (ci/lib/vintner-base.sh): the
# machine key, the signed loader and images, shim, MokManager and the
# certificate on the ESP. The chroot has no UEFI variables, so no request can be
# written there: --secure-boot-enrolled lets the install finish, and row T1
# writes the request from the installed system. P0 boots the result once with
# Secure Boot off, through shim.
if [ ! -f "$OUT/base.env" ] || [ ! -f "$OUT/base.raw" ] || [ ! -f "$OUT/machine-sb/cert.der" ]; then
	VINTNER_INSTALL_ARGS="--secure-boot B --secure-boot-enrolled" ./ci/lib/vintner-base.sh "$FIRMWARE" "$OUT"
fi
# shellcheck source=/dev/null # written by vintner-base.sh
. "$OUT/base.env"
# shellcheck source=ci/lib/vintner-qemu.sh
. ci/lib/vintner-qemu.sh
grep -q "secure boot tier B\|Secure Boot tier B" "$OUT/install.log" || { echo "the base was not installed in tier B" >&2; exit 1; }
CERT_HEX=$(od -An -v -tx1 "$OUT/machine-sb/cert.der" | tr -d ' \n')

# The two Microsoft CAs for db, taken from the signatures of the shim the
# installer put on the ESP (as ci/mok-chain does).
K=$OUT/keys
rm -rf "$K" && mkdir -p "$K"
mcopy -n -i "$BASE_RAW@@$ESP_OFFSET" ::/EFI/vintner/shimx64.efi "$K/shimx64.efi"
python3 - "$K/shimx64.efi" "$K" <<'EOF'
import struct, sys
data = open(sys.argv[1], "rb").read()
pe = struct.unpack_from("<I", data, 0x3C)[0]
opt = pe + 24
dirs = opt + (112 if struct.unpack_from("<H", data, opt)[0] == 0x20B else 96)
start, size = struct.unpack_from("<II", data, dirs + 4 * 8)
off, n = start, 0
while off < start + size:
    length = struct.unpack_from("<I", data, off)[0]
    open(f"{sys.argv[2]}/shim-sig{n}.p7", "wb").write(data[off + 8:off + length])
    n += 1
    off += (length + 7) & ~7
EOF
: > "$K/shim-cas.txt"
for p7 in "$K"/shim-sig*.p7; do
	openssl pkcs7 -inform DER -in "$p7" -print_certs | awk -v base="${p7%.p7}" '
		/^subject=/ { subj = $0 }
		/BEGIN CERTIFICATE/ { n++; f = base "-" n ".pem"; print f "\t" subj >> "'"$K/shim-cas.txt"'" }
		f { print > f }
		/END CERTIFICATE/ { f = "" }'
done
ca() { awk -F'\t' -v cn="CN ?= ?$1\$" '$2 ~ cn { print $1; exit }' "$K/shim-cas.txt"; }
CA2011=$(ca "Microsoft Corporation UEFI CA 2011")
CA2023=$(ca "Microsoft UEFI CA 2023")
if [ -z "$CA2011" ] || [ -z "$CA2023" ]; then echo "shim does not carry both Microsoft CA signatures:" >&2; cat "$K/shim-cas.txt" >&2; exit 1; fi
openssl req -new -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes -subj "/CN=vintner tier B test platform key/" \
	-keyout "$K/pk.key" -out "$K/pk.crt" 2>/dev/null

# --- helpers ------------------------------------------------------------------
# boot_sb LOG TIMEOUT VARS SEED [drive.py steps]: one power-on with the Secure
# Boot firmware; the variable store decides whether Secure Boot is enforced.
boot_sb() {
	local log=$1 timeout=$2 vars=$3 seed=$4 rc=0
	shift 4
	local seedargs=()
	[ -n "$seed" ] && seedargs=(-drive "file=$seed,format=raw,if=virtio")
	python3 ci/mok-chain/drive.py "$log" "$timeout" "$@" -- \
		qemu-system-x86_64 -machine "q35,smm=on,accel=$ACCEL" -m 2048 -smp 2 -display none -serial stdio -monitor none -no-reboot \
		-global driver=cfi.pflash01,property=secure,value=on \
		-drive "if=pflash,format=raw,unit=0,readonly=on,file=$SECBOOT_CODE" \
		-drive "if=pflash,format=raw,unit=1,file=$vars" \
		-drive "file=${BOOT_DISK:-$DISK},format=raw,if=virtio" "${seedargs[@]}" \
		-nic "user,model=virtio-net-pci" || rc=$?
	return "$rc"
}
# var_hex VARS NAME: the data of a shim variable in hex, empty when absent.
var_hex() {
	virt-fw-vars -i "$1" --output-json "$OUT/vars-dump.json" >/dev/null 2>&1
	python3 - "$OUT/vars-dump.json" "$2" "$SHIM_GUID" <<'EOF'
import json, sys
for v in json.load(open(sys.argv[1]))["variables"]:
    if v["name"] == sys.argv[2] and v["guid"] == sys.argv[3]:
        print(v["data"])
EOF
}
# grep reads all of its input in these checks: a grep -q that stops early would
# kill the writer with SIGPIPE, and pipefail would make a match look like a miss.
log_has() { tr -d '\r' < "$1" | grep -a -- "$2" >/dev/null; }
# field LOG NAME: the value of the first "NAME: value" line in a serial log.
field() { tr -d '\r' < "$1" | awk -v p="^$2: " '$0 ~ p && !f { sub(p, ""); v = $0; f = 1 } END { print v }'; }
bless_reports() { tr -d '\r' < "$1" | sed -n '/^BLESS-BEGIN/,/^BLESS-END/p'; }
printf 'ID\tStatus\tEvidence\n' > "$RESULTS_B"
fail=0
row() { # id status evidence
	[ "$2" = PASS ] || fail=1
	printf '%s\t%s\t%s\n' "$1" "$2" "$3" | tee -a "$RESULTS_B"
}
# The guest expands these, not this script.
# shellcheck disable=SC2016
REPORT_OPS='mountpoint -q /sys/firmware/efi/efivars || mount -t efivarfs efivarfs /sys/firmware/efi/efivars
for i in $(seq 120); do s=$(systemctl show -p ActiveState --value vintner-bless.service); [ "$s" = active ] || [ "$s" = failed ] && break; sleep 1; done
{ echo BLESS-BEGIN; journalctl -b -u vintner-bless.service -o cat --no-pager; echo BLESS-END; } > /dev/ttyS0
echo "SB-STATE: $(od -An -tu1 -j4 -N1 /sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c | tr -d " ")" > /dev/ttyS0
echo "KERNEL-SB: $(dmesg | grep -m1 -o "secureboot: Secure boot [a-z]*")" > /dev/ttyS0'

DISK=$OUT/tierb.raw
cp --sparse=always "$BASE_RAW" "$DISK"
VARS=$OUT/tierb-vars.fd
cp "$VARS_TEMPLATE" "$VARS"
chmod 0644 "$VARS"

# --- T1: the installed system writes its request (Secure Boot off) -------------
# The guest boots through shim with Secure Boot off. The health service reports
# both problems of an unenrolled tier B machine; then `vintner secureboot request`
# writes MokNew, MokAuth and MokTimeout through the real efivarfs.
T1_SEED=$(make_seed T1 "$REPORT_OPS
echo $PASSWORD > /root/mokpw
/usr/sbin/vintner secureboot request --password-file /root/mokpw > /dev/ttyS0 2>&1
echo \"REQUEST-RC: \$?\" > /dev/ttyS0")
boot_sb "$OUT/T1.log" 600 "$VARS" "$T1_SEED" || true
REQ=$(var_hex "$VARS" MokNew)
AUTH=$(var_hex "$VARS" MokAuth)
TIMEOUT=$(var_hex "$VARS" MokTimeout)
WANT_AUTH=$(python3 -c 'import hashlib,sys; print(hashlib.sha256(bytes.fromhex(sys.argv[1]) + sys.argv[2].encode("utf-16-le")).hexdigest())' "${REQ:-00}" "$PASSWORD")
evidence="request_rc=$(field "$OUT/T1.log" REQUEST-RC); entry=$(field "$OUT/T1.log" LOADER_ENTRY_SELECTED)"
evidence+="; MokNew_has_cert=$([[ "$REQ" == *"$CERT_HEX"* ]] && echo yes || echo no)"
evidence+="; MokAuth_matches=$([ "$AUTH" = "$WANT_AUTH" ] && echo yes || echo no); MokTimeout=$TIMEOUT"
evidence+="; health_off=$(bless_reports "$OUT/T1.log" | grep -cx 'secure_boot=off' || true)"
evidence+="; health_not_enrolled=$(bless_reports "$OUT/T1.log" | grep -c "not in shim's MOK list" || true)"
if log_has "$OUT/T1.log" "REQUEST-RC: 0" && log_has "$OUT/T1.log" "LOADER_ENTRY_SELECTED: vintner-os-1" \
	&& [[ "$REQ" == *"$CERT_HEX"* ]] && [ "$AUTH" = "$WANT_AUTH" ] && [ "$TIMEOUT" = ffffffff ] \
	&& bless_reports "$OUT/T1.log" | grep -x 'secure_boot=off' >/dev/null && bless_reports "$OUT/T1.log" | grep "not in shim's MOK list" >/dev/null; then
	row T1-request-from-the-system PASS "$evidence"
else
	row T1-request-from-the-system FAIL "$evidence"
fi

# --- T2: Secure Boot on; a person confirms the request in MokManager -----------
# db holds both Microsoft CAs, as on a PC as sold. shim finds the request and
# starts MokManager, which shows its menu at once and waits (MokTimeout -1: no
# "Press any key" countdown); then the steps of ci/mok-chain E1.
virt-fw-vars -i "$VARS" -o "$VARS" --set-pk "$OWNER" "$K/pk.crt" --add-kek "$OWNER" "$K/pk.crt" \
	--add-db "$OWNER" "$CA2011" --add-db "$OWNER" "$CA2023" --secure-boot >/dev/null 2>&1
T2_RC=0
boot_sb "$OUT/T2.log" 240 "$VARS" "" --step "Enroll MOK" "<DN><CR>" \
	--step "View key 0" "<DN><CR>" --step "Enroll the key(s)?" "<DN><CR>" --step "Password" "$PASSWORD<CR>" --step "Reboot" "<CR>" || T2_RC=$?
MOKLIST=$(var_hex "$VARS" MokList)
evidence="steps=$([ "$T2_RC" = 0 ] && echo all || echo "missed (rc $T2_RC)"); MokList_has_cert=$([[ "$MOKLIST" == *"$CERT_HEX"* ]] && echo yes || echo no)"
evidence+="; MokNew_left=$([ -n "$(var_hex "$VARS" MokNew)" ] && echo yes || echo no)"
if [ "$T2_RC" = 0 ] && [[ "$MOKLIST" == *"$CERT_HEX"* ]] && [ -z "$(var_hex "$VARS" MokNew)" ]; then
	row T2-confirm-in-mokmanager PASS "$evidence"
else
	row T2-confirm-in-mokmanager FAIL "$evidence"
fi

# --- T3: the enrolled machine boots with Secure Boot enforced, healthy ---------
T3_SEED=$(make_seed T3 "$REPORT_OPS
echo \"MOKLISTRT: \$(od -An -v -tx1 /sys/firmware/efi/efivars/MokListRT-$SHIM_GUID 2>/dev/null | tr -d ' \n')\$(od -An -v -tx1 /sys/firmware/efi/mok-variables/MokListRT 2>/dev/null | tr -d ' \n')\" > /dev/ttyS0")
boot_sb "$OUT/T3.log" 600 "$VARS" "$T3_SEED" || true
T3=$(tr -d '\r' < "$OUT/T3.log")
evidence="sb=$(field "$OUT/T3.log" SB-STATE); kernel=$(field "$OUT/T3.log" KERNEL-SB)"
evidence+="; entry=$(field "$OUT/T3.log" LOADER_ENTRY_SELECTED); bless=$(field "$OUT/T3.log" BLESS_ACTIVE)"
evidence+="; MokListRT_has_cert=$(grep -a '^MOKLISTRT: ' <<<"$T3" | grep "$CERT_HEX" >/dev/null && echo yes || echo no)"
evidence+="; tier_B_reports=$(bless_reports "$OUT/T3.log" | grep -c 'secure_boot=off\|secure boot tier B' || true)"
if grep -aqx 'SB-STATE: 1' <<<"$T3" && grep -aq 'KERNEL-SB: secureboot: Secure boot enabled' <<<"$T3" \
	&& grep -aq 'LOADER_ENTRY_SELECTED: vintner-os-1' <<<"$T3" && grep -aqx 'BLESS_ACTIVE: active' <<<"$T3" \
	&& grep -a '^MOKLISTRT: ' <<<"$T3" | grep "$CERT_HEX" >/dev/null \
	&& ! bless_reports "$OUT/T3.log" | grep 'secure_boot=off\|secure boot tier B' >/dev/null; then
	row T3-boots-enforced-and-healthy PASS "$evidence"
else
	row T3-boots-enforced-and-healthy FAIL "$evidence"
fi

# --- T4 to T6: out-of-tree modules (WO-0010 5.3) --------------------------------
# DKMS builds a module in the installed system, signs it with the machine key (the
# drop-in `vintner install` wrote), and the rebuild leads to a newly published
# generation. Debian 13's kernel then loads it with Secure Boot on: in tier B
# because the key is a MOK, and with the firmware trusting only db (tier A's
# setup) because the module check falls back to the platform keyring.
# The guest expands everything in these scripts, not this one.
IFS= read -r -d '' GUEST_LIB <<'EOS' || true
P() { echo "$*" > /dev/ttyS0; }
systemctl mask --now serial-getty@ttyS0.service >/dev/null 2>&1
export DEBIAN_FRONTEND=noninteractive
mountpoint -q /sys/firmware/efi/efivars || mount -t efivarfs efivarfs /sys/firmware/efi/efivars
ABI=$(uname -r)
for i in $(seq 120); do s=$(systemctl show -p ActiveState --value vintner-bless.service); [ "$s" = active ] || [ "$s" = failed ] && break; sleep 1; done
vstat() {
  /usr/sbin/vintner status --json > /tmp/st.json 2>/tmp/st.err
  python3 - "$1" <<'PY' > /dev/ttyS0
import json, sys
p = sys.argv[1]
r = json.load(open("/tmp/st.json"))
print(p + "-GENS: " + ";".join("%d:%s:%s:%s" % (g["generation"], g["state"], "stale" if g["stale"] else "fresh", g["abi"]) for g in r["generations"]))
print(p + "-HEALTH: " + ("|".join(r["health"]) or "-"))
m = r["modules"]
print(p + "-MODULES: %s %s %s" % (m["out_of_tree"], str(m.get("requires_signature")).lower(), m.get("reason", "")))
PY
}
common() {
  P "SB-STATE: $(od -An -tu1 -j4 -N1 /sys/firmware/efi/efivars/SecureBoot-8be4df61-93ca-11d2-aa0d-00e098032b8c | tr -d ' ')"
  P "KERNEL-SB: $(dmesg | grep -m1 -o 'secureboot: Secure boot [a-z]*')"
  P "SIG-ENFORCE: $(cat /sys/module/module/parameters/sig_enforce)"
  P "MOKVARS: $(ls /sys/firmware/efi/efivars | grep -ci '^mok')"
  grep -E '\.(platform|machine):' /proc/keys | sed 's/^[0-9a-f]* *[^ ]* *[^ ]* *[^ ]* *[^ ]* *[0-9]* *[0-9]* *//; s/^/KEYRING: /' > /dev/ttyS0
}
loadtest() {
  modprobe vintner_test > /tmp/mp.log 2>&1; P "MODPROBE-RC: $?"
  P "MODPROBE-LOG: $(tr '\n' ' ' < /tmp/mp.log)"
  P "LSMOD: $(lsmod | grep -c '^vintner_test')"
  P "DMESG: $(dmesg | grep -m1 'vintner_test: loaded')"
}
EOS
IFS= read -r -d '' GUEST_T4 <<'EOS' || true
P "=== T4-BEGIN"
vstat PRE
apt-get update -q > /tmp/apt.log 2>&1
apt-get install -y -q dkms build-essential "linux-headers-$ABI" >> /tmp/apt.log 2>&1; P "APT-RC: $?"
tail -3 /tmp/apt.log | sed 's/^/APT-LOG: /' > /dev/ttyS0
vstat MID
mkdir -p /usr/src/vintner-test-1.0
cat > /usr/src/vintner-test-1.0/dkms.conf <<'EOF'
PACKAGE_NAME="vintner-test"
PACKAGE_VERSION="1.0"
BUILT_MODULE_NAME[0]="vintner_test"
DEST_MODULE_LOCATION[0]="/updates/dkms"
AUTOINSTALL="yes"
EOF
printf 'obj-m := vintner_test.o\nKDIR := /lib/modules/$(kernelver)/build\nall:\n\t$(MAKE) -C $(KDIR) M=$(CURDIR) modules\nclean:\n\t$(MAKE) -C $(KDIR) M=$(CURDIR) clean\n' > /usr/src/vintner-test-1.0/Makefile
cat > /usr/src/vintner-test-1.0/vintner_test.c <<'EOF'
#include <linux/module.h>
#include <linux/init.h>
static int __init vt_init(void) { pr_info("vintner_test: loaded\n"); return 0; }
static void __exit vt_exit(void) { pr_info("vintner_test: unloaded\n"); }
module_init(vt_init);
module_exit(vt_exit);
MODULE_LICENSE("GPL");
MODULE_DESCRIPTION("vintner DKMS test module");
EOF
P "DROPIN: $(grep -c '^mok_' /etc/dkms/framework.conf.d/vintner-secureboot.conf) mok lines; $(ls /etc/dkms/framework.conf.d | tr '\n' ' ')"
dkms add -m vintner-test -v 1.0 > /tmp/dkms.log 2>&1; P "DKMS-ADD-RC: $?"
dkms install -m vintner-test -v 1.0 -k "$ABI" >> /tmp/dkms.log 2>&1; P "DKMS-INSTALL-RC: $?"
sed 's/^/DKMS-LOG: /' /tmp/dkms.log > /dev/ttyS0
sed 's/^/POSTTX: /' /var/lib/dkms/post_transaction.log > /dev/ttyS0 2>&1
vstat POST
M=$(find /lib/modules/$ABI/updates -name 'vintner_test.ko*' | head -1)
P "MODULE-FILE: $M"
P "MODINFO-SIGNER: $(modinfo -F signer "$M")"
P "MODINFO-HASH: $(modinfo -F sig_hashalgo "$M")"
common
loadtest
modprobe -r vintner_test

# Controls. A module that DKMS signed with a key nothing trusts, and one that is not
# signed at all, must be refused, or "loads" proves nothing.
openssl req -new -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes -subj "/CN=foreign dkms key/" -addext extendedKeyUsage=codeSigning -keyout /root/foreign.key -out /root/foreign.crt 2>/dev/null
openssl x509 -in /root/foreign.crt -outform DER -out /root/foreign.der
mkdir -p /usr/src/vintner-foreign-1.0
sed 's/vintner_test/vintner_foreign/g; s/vintner-test/vintner-foreign/g' /usr/src/vintner-test-1.0/dkms.conf > /usr/src/vintner-foreign-1.0/dkms.conf
sed 's/vintner_test/vintner_foreign/g' /usr/src/vintner-test-1.0/Makefile > /usr/src/vintner-foreign-1.0/Makefile
sed 's/vintner_test/vintner_foreign/g' /usr/src/vintner-test-1.0/vintner_test.c > /usr/src/vintner-foreign-1.0/vintner_foreign.c
printf 'mok_signing_key=/root/foreign.key\nmok_certificate=/root/foreign.der\n' > /etc/dkms/framework.conf.d/zz-foreign.conf
dkms add -m vintner-foreign -v 1.0 > /tmp/dkms2.log 2>&1
dkms install -m vintner-foreign -v 1.0 -k "$ABI" >> /tmp/dkms2.log 2>&1; P "FOREIGN-INSTALL-RC: $?"
modprobe vintner_foreign > /tmp/mp2.log 2>&1; P "FOREIGN-MODPROBE-RC: $?"
P "FOREIGN-DMESG: $(dmesg | grep -m1 'unavailable key')"
dkms remove -m vintner-foreign -v 1.0 --all >> /tmp/dkms2.log 2>&1
rm -f /etc/dkms/framework.conf.d/zz-foreign.conf
mkdir -p /tmp/unsigned && cp /usr/src/vintner-test-1.0/vintner_test.c /tmp/unsigned/ && printf 'obj-m := vintner_test.o\n' > /tmp/unsigned/Makefile
make -C "/lib/modules/$ABI/build" M=/tmp/unsigned modules > /tmp/unsigned.log 2>&1; P "UNSIGNED-BUILD-RC: $?"
insmod /tmp/unsigned/vintner_test.ko > /tmp/mp3.log 2>&1; P "UNSIGNED-INSMOD-RC: $?"
P "UNSIGNED-DMESG: $(dmesg | grep -m1 'unsigned module is rejected')"
vstat END
EOS
IFS= read -r -d '' GUEST_T5 <<'EOS' || true
P "=== T5-BEGIN"
common
vstat T5
loadtest
EOS
T4_SEED=$(make_seed T4 "$GUEST_LIB
$GUEST_T4")
boot_sb "$OUT/T4.log" 1800 "$VARS" "$T4_SEED" || true
f4() { field "$OUT/T4.log" "$1"; }
f4_has() { f4 "$1" | grep "$2" >/dev/null; }
evidence="apt=$(f4 APT-RC); dkms=$(f4 DKMS-INSTALL-RC); signer='$(f4 MODINFO-SIGNER)'; modprobe=$(f4 MODPROBE-RC) lsmod=$(f4 LSMOD); sb=$(f4 SB-STATE)"
evidence+="; foreign_rc=$(f4 FOREIGN-MODPROBE-RC) unsigned_rc=$(f4 UNSIGNED-INSMOD-RC)"
evidence+="; gens PRE=$(f4 PRE-GENS) MID=$(f4 MID-GENS) POST=$(f4 POST-GENS)"
evidence+="; post_tx=$(tr -d '\r' < "$OUT/T4.log" | grep -a -c '^POSTTX: .*module tree changed (dkms' || true)"
if [ "$(f4 APT-RC)" = 0 ] && [ "$(f4 DKMS-INSTALL-RC)" = 0 ] && f4_has MODINFO-SIGNER 'vintner machine' \
	&& [ "$(f4 SB-STATE)" = 1 ] && [ "$(f4 MODPROBE-RC)" = 0 ] && [ "$(f4 LSMOD)" = 1 ] \
	&& [ "$(f4 FOREIGN-MODPROBE-RC)" != 0 ] && [ -n "$(f4 FOREIGN-DMESG)" ] \
	&& [ "$(f4 UNSIGNED-INSMOD-RC)" != 0 ] && [ -n "$(f4 UNSIGNED-DMESG)" ]; then
	row T4-dkms-signs-and-loads PASS "$evidence"
else
	row T4-dkms-signs-and-loads FAIL "$evidence"
fi
# The rebuild led to a newly published generation: the newest image is fresh, the
# one that predates the module is stale or gone, and the health report has no
# out-of-date line. DKMS ran the publish itself (post_transaction).
newest=$(f4 POST-GENS | tr ';' '\n' | sort -t: -k1,1nr | head -1)
if [ -n "$newest" ] && [[ "$newest" == *":fresh:"* ]] && [[ "$newest" != "$(f4 PRE-GENS | tr ';' '\n' | sort -t: -k1,1nr | head -1)" ]] \
	&& ! f4_has POST-HEALTH 'out of date' && ! f4_has END-HEALTH 'out of date' \
	&& [ "$(tr -d '\r' < "$OUT/T4.log" | grep -a -c '^POSTTX: .*module tree changed (dkms')" -ge 1 ]; then
	row T4b-rebuild-publishes-an-image PASS "newest=$newest; $evidence"
else
	row T4b-rebuild-publishes-an-image FAIL "newest=$newest; $evidence"
fi

# --- T5: the next boot starts that generation and loads the module ---------------
T5_SEED=$(make_seed T5 "$GUEST_LIB
$GUEST_T5")
boot_sb "$OUT/T5.log" 600 "$VARS" "$T5_SEED" || true
f5() { field "$OUT/T5.log" "$1"; }
evidence="sb=$(f5 SB-STATE); entry=$(f5 LOADER_ENTRY_SELECTED); modprobe=$(f5 MODPROBE-RC) lsmod=$(f5 LSMOD); bless=$(f5 BLESS_ACTIVE); gens=$(f5 T5-GENS); modules='$(f5 T5-MODULES)'"
newest_gen=$(f4 POST-GENS | tr ';' '\n' | sort -t: -k1,1nr | head -1 | cut -d: -f1)
if [ "$(f5 SB-STATE)" = 1 ] && [ "$(f5 MODPROBE-RC)" = 0 ] && [ "$(f5 LSMOD)" = 1 ] \
	&& [[ "$(f5 LOADER_ENTRY_SELECTED)" == "vintner-os-$newest_gen"* ]] && [ "$(f5 BLESS_ACTIVE)" = active ] \
	&& [[ "$(f5 T5-MODULES)" == "allowed true "* ]] && ! f5 T5-HEALTH | grep 'out of date' >/dev/null; then
	row T5-new-generation-boots-and-loads-the-module PASS "$evidence"
else
	row T5-new-generation-boots-and-loads-the-module FAIL "$evidence"
fi

# --- T6: the firmware trusts only db (tier A's setup): the same module loads ------
# No shim and no MokList: the machine certificate is the only db entry, systemd-boot
# is started directly, and the kernel's .machine keyring is empty. The module check
# falls back to the .platform keyring, which holds db's certificates.
TA_DISK=$OUT/tiera.raw
cp --sparse=always "$DISK" "$TA_DISK"
mcopy -n -i "$TA_DISK@@$ESP_OFFSET" ::/EFI/vintner/grubx64.efi "$OUT/loader-machine-signed.efi"
mcopy -o -i "$TA_DISK@@$ESP_OFFSET" "$OUT/loader-machine-signed.efi" ::/EFI/BOOT/BOOTX64.EFI
virt-fw-vars -i "$VARS_TEMPLATE" -o "$OUT/tiera-vars.fd" --set-pk "$OWNER" "$K/pk.crt" --add-kek "$OWNER" "$K/pk.crt" \
	--add-db "$OWNER" "$OUT/machine-sb/cert.pem" --secure-boot >/dev/null 2>&1
T6_SEED=$(make_seed T6 "$GUEST_LIB
$GUEST_T5")
BOOT_DISK=$TA_DISK boot_sb "$OUT/T6.log" 600 "$OUT/tiera-vars.fd" "$T6_SEED" || true
f6() { field "$OUT/T6.log" "$1"; }
keyrings=$(tr -d '\r' < "$OUT/T6.log" | grep -a '^KEYRING: ' | tr -s ' ' | tr '\n' ' ' || true)
evidence="sb=$(f6 SB-STATE); mokvars=$(f6 MOKVARS); modprobe=$(f6 MODPROBE-RC) lsmod=$(f6 LSMOD); keyrings: $keyrings"
if [ "$(f6 SB-STATE)" = 1 ] && [ "$(f6 MOKVARS)" = 0 ] && [ "$(f6 MODPROBE-RC)" = 0 ] && [ "$(f6 LSMOD)" = 1 ] \
	&& [[ "$keyrings" == *"keyring .machine: empty"* ]] && [[ "$keyrings" == *"keyring .platform: 1"* ]]; then
	row T6-db-only-trust-loads-the-module PASS "$evidence"
else
	row T6-db-only-trust-loads-the-module FAIL "$evidence"
fi
{
	echo "=== Host packages (tier B) ==="
	dpkg-query -W ovmf qemu-system-x86 python3-virt-firmware sbsigntool 2>/dev/null || true
	echo "shim's signers:"; cut -f2 "$K/shim-cas.txt"
} >> "$VERSIONS"
cat "$RESULTS_B"
exit $fail
