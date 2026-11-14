#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Proves the Secure Boot chain of tier B (WO-0010 5.2, architecture §8 and §12
# risk 1) on Debian 13's own binaries under QEMU and OVMF with Secure Boot
# enforced: Debian's Microsoft-signed shim starts a systemd-boot and a UKI that
# are signed with a machine key, when and only when that key's certificate is a
# Machine Owner Key (MOK). It also proves how the certificate gets there
# (MokManager, with a password or from a file), what the Microsoft 2011 and 2023
# CAs in db do, and that SBAT and dbx revoke. See README.md for the rows.
# Needs: curl, xz, dpkg-deb, cpio, gzip, sfdisk, mtools, openssl, sbsigntool,
# python3 with pefile, python3-virt-firmware, qemu-system-x86_64 and the OVMF 4M
# firmware with its Secure Boot build. No root.
set -euo pipefail
cd "$(dirname "$0")/../.."

OUT=ci/out/mok-chain
ROOT=$OUT/root
MIRROR=https://deb.debian.org/debian
CODE=/usr/share/OVMF/OVMF_CODE_4M.secboot.fd
VARS=/usr/share/OVMF/OVMF_VARS_4M.fd
SHIM_GUID=605dab50-e046-4300-abb6-3dd810dd8b23
OWNER=bbbbbbbb-cccc-dddd-eeee-ffffffffffff
PASSWORD=vintner1
export MTOOLS_SKIP_CHECK=1
mkdir -p "$OUT/debs" "$ROOT"
: > "$OUT/versions.txt"
SUMMARY=$OUT/summary.tsv
: > "$SUMMARY"
fail=0

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
for p in shim-signed shim-helpers-amd64-signed systemd-boot-efi systemd-boot-efi-amd64-signed \
	systemd-ukify busybox-static "$kernel_pkg"; do fetch "$p"; done
first() { local f; for f; do [ -e "$f" ] && { echo "$f"; return; }; done; return 1; }
KERNEL=$(ls "$ROOT"/boot/vmlinuz-*)
STUB=$ROOT/usr/lib/systemd/boot/efi/linuxx64.efi.stub
SDBOOT=$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi
SDBOOT_DEBIAN=$ROOT/usr/lib/systemd/boot/efi/systemd-bootx64.efi.signed
SHIM=$ROOT/usr/lib/shim/shimx64.efi.signed
MOKMANAGER=$ROOT/usr/lib/shim/mmx64.efi.signed
UKIFY=$(first "$ROOT"/usr/lib/systemd/ukify "$ROOT"/usr/bin/ukify)
BUSYBOX=$(first "$ROOT"/usr/bin/busybox "$ROOT"/bin/busybox)

# --- keys: the machine key, a foreign key, and a test platform key ----------
K=$OUT/keys
rm -rf "$K" && mkdir -p "$K"
mkkey() { # name CN
	openssl req -new -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes -subj "/CN=$2/" \
		-addext "extendedKeyUsage=codeSigning" -keyout "$K/$1.key" -out "$K/$1.crt" 2>/dev/null
	openssl x509 -in "$K/$1.crt" -outform DER -out "$K/$1.der"
}
mkkey machine "vintner machine mok-chain test"
mkkey foreign "foreign key"
mkkey pk "vintner mok-chain test platform key"

# The two Microsoft CAs, taken from the two signatures that shim carries.
python3 - "$SHIM" "$K" <<'EOF'
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
ca() { awk -F'\t' -v cn="CN=$1\$" '$2 ~ cn { print $1; exit }' "$K/shim-cas.txt"; }
CA2011=$(ca "Microsoft Corporation UEFI CA 2011")
CA2023=$(ca "Microsoft UEFI CA 2023")
if [ -z "$CA2011" ] || [ -z "$CA2023" ]; then echo "shim does not carry both Microsoft CA signatures:" >&2; cat "$K/shim-cas.txt" >&2; exit 1; fi

# --- test initramfs: print the test id, then power off ----------------------
rm -rf "$OUT/initrd" && mkdir -p "$OUT/initrd"/{bin,proc,dev}
cp "$BUSYBOX" "$OUT/initrd/bin/busybox"
cat > "$OUT/initrd/init" <<'EOF'
#!/bin/busybox sh
/bin/busybox mount -t proc proc /proc
/bin/busybox mount -t devtmpfs dev /dev
echo "VINTNER-TEST-BOOTED id=$(/bin/busybox sed -n 's/.*vintner.test_id=\([^ ]*\).*/\1/p' /proc/cmdline)" > /dev/ttyS0
/bin/busybox poweroff -f
EOF
chmod +x "$OUT/initrd/init"
(cd "$OUT/initrd" && find . | cpio -o -H newc --owner=0:0 --quiet | gzip -9) > "$OUT/initrd.img"

# --- loader and UKIs --------------------------------------------------------
printf 'ID=debian\nIMAGE_ID=vintner-os\nIMAGE_VERSION=1\nPRETTY_NAME="Test OS"\n' > "$OUT/osrel"
python3 "$UKIFY" build --linux="$KERNEL" --initrd="$OUT/initrd.img" --stub="$STUB" --os-release="@$OUT/osrel" \
	--cmdline="console=ttyS0 panic=-1 vintner.test_id=os-1" --output="$OUT/uki-unsigned.efi" >/dev/null
sign() { sbsign --key "$K/$1.key" --cert "$K/$1.crt" --output "$3" "$2" >/dev/null 2>&1; }
sign machine "$OUT/uki-unsigned.efi" "$OUT/uki-machine.efi"
sign foreign "$OUT/uki-unsigned.efi" "$OUT/uki-foreign.efi"
sign machine "$SDBOOT" "$OUT/loader-machine.efi"
cp "$SDBOOT" "$OUT/loader-unsigned.efi"
cp "$SDBOOT_DEBIAN" "$OUT/loader-debian.efi"
sbverify --cert "$K/machine.crt" "$OUT/loader-machine.efi" >/dev/null 2>&1
sbverify --cert "$K/machine.crt" "$OUT/uki-machine.efi" >/dev/null 2>&1

# --- disk: GPT with one ESP -------------------------------------------------
# shim is the firmware's boot file. It starts grubx64.efi from its own directory
# (its built-in name for the second stage) and MokManager as mmx64.efi. The
# machine certificate lies on the ESP for MokManager's "Enroll key from disk".
IMG=$OUT/disk.img
new_disk() { # loader uki
	rm -f "$IMG" && truncate -s 200M "$IMG"
	printf 'label: gpt\ntype=C12A7328-F81F-11D2-BA4B-00A0C93EC93B\n' | sfdisk -q "$IMG"
	ESP="$IMG@@$(( $(sfdisk -J "$IMG" | python3 -c 'import json,sys; print(json.load(sys.stdin)["partitiontable"]["partitions"][0]["start"])') * 512 ))"
	mformat -i "$ESP" -F -v ESP ::
	mmd -i "$ESP" ::/EFI ::/EFI/BOOT ::/EFI/Linux ::/loader
	mcopy -i "$ESP" "$SHIM" ::/EFI/BOOT/BOOTX64.EFI
	mcopy -i "$ESP" "$MOKMANAGER" ::/EFI/BOOT/mmx64.efi
	mcopy -i "$ESP" "$OUT/loader-$1.efi" ::/EFI/BOOT/grubx64.efi
	mcopy -i "$ESP" "$OUT/uki-$2.efi" ::/EFI/Linux/vintner-os-1.efi
	mcopy -i "$ESP" firmware/uefi/loader.conf ::/loader/loader.conf
	mcopy -i "$ESP" "$K/machine.der" ::/vintner-machine.cer
}

# --- variable stores --------------------------------------------------------
# new_vars "<db certificates>" [mok] [request] [wait] [sbat] [dbx]
#   mok      the machine certificate is in MokList (enrolled)
#   request  MokNew and MokAuth: a pending request, as an installer writes it
#   wait     MokTimeout -1: MokManager waits for a key with no time limit
#   sbat     SbatLevel revokes every systemd-boot
#   dbx      the machine certificate is in dbx
new_vars() {
	local db=$1 args=() c
	shift
	for c in $db; do args+=(--add-db "$OWNER" "$c"); done
	virt-fw-vars -i "$VARS" -o "$OUT/vars.fd" --set-pk "$OWNER" "$K/pk.crt" --add-kek "$OWNER" "$K/pk.crt" \
		"${args[@]}" --secure-boot >/dev/null 2>&1
	python3 - "$K/machine.der" "$SHIM_GUID" "$PASSWORD" "$@" > "$OUT/vars.json" <<'EOF'
import hashlib, json, sys, uuid
der, shim, password, flags = open(sys.argv[1], "rb").read(), sys.argv[2], sys.argv[3], sys.argv[4:]
x509 = uuid.UUID("a5c059a1-94e4-4aa7-87b5-ab155c2bf072").bytes_le
size = 16 + len(der)
# EFI_SIGNATURE_LIST with one EFI_SIGNATURE_DATA: the certificate, owned by shim's GUID.
esl = x509 + (28 + size).to_bytes(4, "little") + bytes(4) + size.to_bytes(4, "little") + uuid.UUID(shim).bytes_le + der
out = []
def var(name, attr, data, guid=shim):
    out.append({"name": name, "guid": guid, "attr": attr, "data": data.hex()})
if "mok" in flags:
    var("MokList", 3, esl)  # non-volatile, boot services only: what MokManager writes
if "request" in flags:
    var("MokNew", 7, esl)
    # MokManager's plain form of MokAuth: SHA-256 over MokNew and the password in UTF-16.
    var("MokAuth", 7, hashlib.sha256(esl + password.encode("utf-16-le")).digest())
if "wait" in flags:
    var("MokTimeout", 7, (-1).to_bytes(4, "little", signed=True))
if "sbat" in flags:
    var("SbatLevel", 3, b"sbat,1,2099010100\nsystemd-boot,99\n")
if "dbx" in flags:
    var("dbx", 0x27, esl, "d719b2cb-3d3a-4596-a3bc-dad00e67656f")
print(json.dumps({"version": 2, "variables": out}))
EOF
	virt-fw-vars -i "$OUT/vars.fd" -o "$OUT/vars.fd" --set-json "$OUT/vars.json" >/dev/null 2>&1
}
has_var() { virt-fw-vars -i "$OUT/vars.fd" --print 2>/dev/null | grep -aq "^$1 "; }

ACCEL=tcg
[ -w /dev/kvm ] && ACCEL=kvm
BOOT_TIMEOUT=120
boot() { # log [drive.py steps]: one power-on; prints "<id or NONE> <sb or -> <shim's refusal or ->"
	local log=$OUT/$1.log rc=0 id sb refused
	shift
	python3 ci/mok-chain/drive.py "$log" "$BOOT_TIMEOUT" "$@" -- \
		qemu-system-x86_64 -machine "q35,smm=on,accel=$ACCEL" -m 1024 -display none -serial stdio -monitor none -no-reboot \
		-global driver=cfi.pflash01,property=secure,value=on \
		-drive "if=pflash,format=raw,unit=0,readonly=on,file=$CODE" \
		-drive "if=pflash,format=raw,unit=1,file=$OUT/vars.fd" \
		-drive "format=raw,file=$IMG,if=virtio" || rc=$?
	id=$(grep -a -m1 -o 'VINTNER-TEST-BOOTED id=[a-z0-9-]*' "$log" | sed 's/.*id=//' || true)
	sb=$(grep -a -m1 -o 'secureboot: Secure boot [a-z]*' "$log" | sed 's/.* //' || true)
	refused=-
	grep -aq 'Security Violation' "$log" && refused=violation
	[ "$rc" = 0 ] || refused="$refused,keys-not-sent"
	echo "${id:-NONE} ${sb:--} $refused"
}
row() { # name observed expected
	local result=PASS
	[ "$2" = "$3" ] || { result=FAIL; fail=1; echo "$1: observed '$2', expected '$3'" >&2; }
	printf '%s\t%s\t%s\t%s\n' "$1" "$2" "$3" "$result" | tee -a "$SUMMARY"
}
MS="$CA2011 $CA2023"

# --- M: the chain -----------------------------------------------------------
new_disk machine machine; new_vars "$MS"
row M0-no-mok "$(boot M0)" "NONE - violation"
new_disk machine machine; new_vars "$MS" mok
row M1-mok "$(boot M1)" "os-1 enabled -"
new_disk machine foreign; new_vars "$MS" mok
row M2-foreign-uki "$(boot M2)" "NONE - violation"
new_disk machine unsigned; new_vars "$MS" mok
row M3-unsigned-uki "$(boot M3)" "NONE - violation"
new_disk unsigned machine; new_vars "$MS" mok
row M4-unsigned-loader "$(boot M4)" "NONE - violation"
# Debian's own signed systemd-boot, which shim trusts through its built-in Debian
# CA, also starts a UKI that only the MOK vouches for.
new_disk debian machine; new_vars "$MS" mok
row M5-debian-loader-mok "$(boot M5)" "os-1 enabled -"
new_disk debian machine; new_vars "$MS"
row M6-debian-loader-no-mok "$(boot M6)" "NONE - violation"

# --- C: the Microsoft CAs in db (architecture §8, "Microsoft CA transition") --
new_disk machine machine; new_vars "$CA2011" mok
row C1-db-2011-only "$(boot C1)" "os-1 enabled -"
new_disk machine machine; new_vars "$CA2023" mok
row C2-db-2023-only "$(boot C2)" "os-1 enabled -"
# Neither CA: the firmware refuses shim itself, so shim prints nothing.
new_disk machine machine; new_vars "$K/pk.crt" mok
row C3-db-no-microsoft-ca "$(boot C3)" "NONE - -"

# --- R: revocation ----------------------------------------------------------
new_disk machine machine; new_vars "$MS" mok sbat
row R1-sbat-revokes-loader "$(boot R1)" "NONE - violation"
new_disk machine machine; new_vars "$MS" mok dbx
row R2-machine-cert-in-dbx "$(boot R2)" "NONE - violation"

# --- E: enrollment in MokManager --------------------------------------------
BOOT_TIMEOUT=180
# E1: the request is confirmed with the password; the next power-on boots.
new_disk machine machine; new_vars "$MS" request
got=$(boot E1-boot1 --step "Press any key to perform MOK management" " " --step "Enroll MOK" "<DN><CR>" \
	--step "View key 0" "<DN><CR>" --step "Enroll the key(s)?" "<DN><CR>" --step "Password" "$PASSWORD<CR>" --step "Reboot" "<CR>")
row E1-confirm-with-password "$got | $(boot E1-boot2)" "NONE - - | os-1 enabled -"

# E2: nobody presses a key. After MokManager's 10 seconds shim deletes the
# request and goes on to the loader, which it refuses.
new_disk machine machine; new_vars "$MS" request
got=$(BOOT_TIMEOUT=60 boot E2)
row E2-no-key-request-lost "$got request=$(has_var MokAuth && echo kept || echo gone)" "NONE - violation request=gone"

# E3: with MokTimeout -1 MokManager waits, and the request is still there.
new_disk machine machine; new_vars "$MS" request wait
got=$(BOOT_TIMEOUT=60 boot E3)
row E3-no-key-waits "$got request=$(has_var MokAuth && echo kept || echo gone) menu=$(grep -aq 'Booting in' "$OUT/E3.log" && echo countdown || echo waiting)" \
	"NONE - - request=kept menu=waiting"

# E4: no request at all. shim refuses the loader and offers MokManager, where the
# certificate file on the ESP is enrolled; the next power-on boots.
new_disk machine machine; new_vars "$MS"
got=$(boot E4-boot1 --step "Security Violation" "<CR>" --step "Press any key to perform MOK management" " " \
	--step "Enroll key from disk" "<DN><CR>" --step "Remember to make sure it is a genuine key" "<CR>" --step "vintner-machine.cer" "<DN><DN><CR>" \
	--step "View key 0" "<DN><CR>" --step "Enroll the key(s)?" "<DN><CR>" --step "Reboot" "<CR>")
row E4-enroll-from-disk "$got | $(boot E4-boot2)" "NONE - violation | os-1 enabled -"

# E5: a wrong password three times enrolls nothing.
new_disk machine machine; new_vars "$MS" request
got=$(BOOT_TIMEOUT=60 boot E5-boot1 --step "Press any key to perform MOK management" " " --step "Enroll MOK" "<DN><CR>" \
	--step "View key 0" "<DN><CR>" --step "Enroll the key(s)?" "<DN><CR>" \
	--step "Password" "wrong<CR><WAIT><WAIT>wrong<CR><WAIT><WAIT>wrong<CR><WAIT><WAIT><CR>")
row E5-wrong-password "$(echo "$got" | cut -d' ' -f1,2) | $(BOOT_TIMEOUT=60 boot E5-boot2 | cut -d' ' -f1,2)" "NONE - | NONE -"

echo "trixie packages:"; cat "$OUT/versions.txt"
echo "shim's signers:"; cut -f2 "$K/shim-cas.txt"
dpkg-query -W ovmf qemu-system-x86 python3-virt-firmware sbsigntool 2>/dev/null || true
exit $fail
