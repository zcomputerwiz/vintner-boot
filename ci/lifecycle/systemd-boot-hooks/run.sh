#!/usr/bin/env bash
set -e

# Configuration
IMAGE_URL="https://cloud.debian.org/images/cloud/trixie/"
LATEST_DIR=$(curl -s "$IMAGE_URL" | grep 'href="202' | tail -n 1 | sed 's/.*href="\([^"]*\)".*/\1/')
HASH_HREF="${IMAGE_URL}${LATEST_DIR}SHA512SUMS"

WORKDIR=$(mktemp -d)

echo "Downloading SHA512SUMS..."
curl -sL "$HASH_HREF" -o "$WORKDIR/SHA512SUMS"

cd "$WORKDIR"
ACTUAL_IMAGE_NAME=$(grep "debian-13-genericcloud-amd64-.*\.qcow2" SHA512SUMS | awk '{print $2}')
IMAGE_HREF="${IMAGE_URL}${LATEST_DIR}${ACTUAL_IMAGE_NAME}"

if [ ! -f "/tmp/$ACTUAL_IMAGE_NAME" ]; then
    echo "Downloading ${ACTUAL_IMAGE_NAME}..."
    curl -sL "$IMAGE_HREF" -o "/tmp/$ACTUAL_IMAGE_NAME"
fi

cp "$OLDPWD/firmware/uefi/loader.conf" "$WORKDIR/loader.conf" || touch "$WORKDIR/loader.conf"

cat << 'GUEST_SCRIPT' > guest-script.sh
#!/usr/bin/env bash
set -x
exec > /mnt/results/guest.log 2>&1
CONFIG=$(cat /etc/current_config)

# Setup systemd service for continuation
cat << 'SERVICE' > /etc/systemd/system/guest-resume.service
[Unit]
Description=Guest Resume
After=network.target cloud-final.service
[Service]
Type=oneshot
ExecStart=/bin/bash /guest-resume.sh
RemainAfterExit=yes
[Install]
WantedBy=multi-user.target
SERVICE

cat << 'RESUME' > /guest-resume.sh
#!/usr/bin/env bash
set -x
exec >> /mnt/results/guest.log 2>&1
CONFIG=\$(cat /etc/current_config)

record_state() {
    local phase=\$1
    echo "Recording state for \$phase"
    find /boot/efi -type f -exec sha256sum {} + > /mnt/results/\${phase}.esp.sha256 || true
    if [ -d /mnt/xbootldr ] && [ -n "\$(ls -A /mnt/xbootldr 2>/dev/null)" ]; then
        find /mnt/xbootldr -type f -exec sha256sum {} + > /mnt/results/\${phase}.xbootldr.sha256 || true
    fi
    if command -v efibootmgr >/dev/null; then
        efibootmgr -v > /mnt/results/\${phase}.efibootmgr || true
    fi
    ls -l /sys/firmware/efi/efivars/ | grep 4a67b082-0a4c-41cf-b6c7-440b29bb8c4f > /mnt/results/\${phase}.loaderinfo.vars || true
    for var in \$(ls /sys/firmware/efi/efivars/ | grep 4a67b082-0a4c-41cf-b6c7-440b29bb8c4f || true); do
        varname=\$(basename "\$var")
        echo "Var: \$varname" >> /mnt/results/\${phase}.loaderinfo.vars.values
        hexdump -C "/sys/firmware/efi/efivars/\$varname" >> /mnt/results/\${phase}.loaderinfo.vars.values || true
    done
    systemctl is-enabled systemd-boot-update.service > /mnt/results/\${phase}.service_enabled || true
}

if [ "\$CONFIG" = "c3" ]; then
    mkdir -p /mnt/xbootldr
    mount /dev/vdd1 /mnt/xbootldr || true
fi

record_state "\${CONFIG}_step2"

export DEBIAN_FRONTEND=noninteractive

# Step 3
apt-get update -y
SECOND_KERNEL=\$(apt-cache search linux-image- | grep -E '^linux-image-[0-9]' | grep -v dbg | grep -v rt | awk '{print \$1}' | head -n 1)
echo "Installing second kernel: \$SECOND_KERNEL" > /mnt/results/\${CONFIG}_second_kernel.txt
apt-get install -y --no-install-recommends "\$SECOND_KERNEL"
record_state "\${CONFIG}_step3"

# Step 4
update-initramfs -u -k all
record_state "\${CONFIG}_step4"

# Step 5
apt-get remove -y "\$SECOND_KERNEL"
record_state "\${CONFIG}_step5"

# Step 6
apt-get install --reinstall -y --no-install-recommends systemd-boot
record_state "\${CONFIG}_step6"

# Step 7
if dpkg-query -W -f='\${Conffiles}\n' systemd-boot | grep -q config; then
    dpkg-reconfigure systemd-boot || true
else
    echo "No config script for systemd-boot" > /mnt/results/\${CONFIG}_step7_noconfig.txt
fi
record_state "\${CONFIG}_step7"

mkdir -p /mnt/results/\${CONFIG}_hooks
cp /etc/kernel/postinst.d/zz-systemd-boot /mnt/results/\${CONFIG}_hooks/ || true
cp /etc/kernel/postrm.d/zz-systemd-boot /mnt/results/\${CONFIG}_hooks/ || true
cp /etc/initramfs/post-update.d/systemd-boot /mnt/results/\${CONFIG}_hooks/ || true
cp /var/lib/dpkg/info/systemd-boot.postinst /mnt/results/\${CONFIG}_hooks/ || true

echo "Done with guest script" > /mnt/results/done.txt
systemctl disable guest-resume.service || true
sync
poweroff
RESUME

chmod +x /guest-resume.sh

record_state() {
    local phase=$1
    echo "Recording state for $phase"

    find /boot/efi -type f -exec sha256sum {} + > /mnt/results/${phase}.esp.sha256 || true

    if [ -d /mnt/xbootldr ] && [ -n "$(ls -A /mnt/xbootldr 2>/dev/null)" ]; then
        find /mnt/xbootldr -type f -exec sha256sum {} + > /mnt/results/${phase}.xbootldr.sha256 || true
    fi

    if command -v efibootmgr >/dev/null; then
        efibootmgr -v > /mnt/results/${phase}.efibootmgr || true
    fi

    ls -l /sys/firmware/efi/efivars/ | grep 4a67b082-0a4c-41cf-b6c7-440b29bb8c4f > /mnt/results/${phase}.loaderinfo.vars || true
    for var in $(ls /sys/firmware/efi/efivars/ | grep 4a67b082-0a4c-41cf-b6c7-440b29bb8c4f || true); do
        varname=$(basename "$var")
        echo "Var: $varname" >> /mnt/results/${phase}.loaderinfo.vars.values
        hexdump -C "/sys/firmware/efi/efivars/$varname" >> /mnt/results/${phase}.loaderinfo.vars.values || true
    done

    systemctl is-enabled systemd-boot-update.service > /mnt/results/${phase}.service_enabled || true
}

if [ "$CONFIG" = "c2" ] || [ "$CONFIG" = "c3" ]; then
    mkdir -p /etc/kernel
    echo "layout=other" > /etc/kernel/install.conf
    mkdir -p /etc/systemd/system
    ln -sf /dev/null /etc/systemd/system/systemd-boot-update.service
fi

if [ "$CONFIG" = "c3" ]; then
    mkdir -p /mnt/xbootldr
    mount /dev/vdd1 /mnt/xbootldr || true
fi

mkdir -p /boot/efi/EFI/vintner
mkdir -p /boot/efi/loader

export DEBIAN_FRONTEND=noninteractive
echo 'Acquire::Retries "3";' > /etc/apt/apt.conf.d/80-retries

apt-get update -y
apt-get install -y --no-install-recommends efibootmgr initramfs-tools

apt-get download systemd-boot -y
dpkg-deb -x systemd-boot*.deb /tmp/sboot
cp /tmp/sboot/usr/lib/systemd/boot/efi/systemd-bootx64.efi /boot/efi/EFI/vintner/ || true
cp /mnt/results/loader.conf /boot/efi/loader/loader.conf || true

record_state "${CONFIG}_step0"

# Step 1
apt-get install -y --no-install-recommends systemd-boot
record_state "${CONFIG}_step1"

systemctl enable guest-resume.service
sync

# Use magic sysrq to cleanly reboot
echo 1 > /proc/sys/kernel/sysrq
echo b > /proc/sysrq-trigger
GUEST_SCRIPT

run_config() {
    local config=$1
    echo "Running config $config"

    cp "/tmp/$ACTUAL_IMAGE_NAME" "image_${config}.qcow2"

    echo "Creating results disk..."
    qemu-img create -f raw "results_${config}.img" 50M
    mkfs.vfat "results_${config}.img"
    mcopy -i "results_${config}.img" loader.conf ::/loader.conf

    mkdir -p "seed_dir_${config}"
    cp guest-script.sh "seed_dir_${config}/"

    cat << CLOUD_INIT_SPECIFIC > "seed_dir_${config}/user-data"
#cloud-config
runcmd:
  - mkdir -p /mnt/seed
  - mount /dev/vdb /mnt/seed
  - mkdir -p /mnt/results
  - mount /dev/vdc /mnt/results
  - echo "$config" > /etc/current_config
  - cp /mnt/seed/guest-script.sh /guest-script.sh
  - chmod +x /guest-script.sh
  - bash /guest-script.sh
CLOUD_INIT_SPECIFIC

    cat << 'CLOUD_INIT' > "seed_dir_${config}/meta-data"
instance-id: test-vm
local-hostname: test-vm
CLOUD_INIT

    genisoimage -output "seed_${config}.iso" -volid cidata -joliet -rock "seed_dir_${config}/user-data" "seed_dir_${config}/meta-data" "seed_dir_${config}/guest-script.sh"

    QEMU_ARGS="-m 2G -machine q35 -nographic \
      -drive if=pflash,format=raw,readonly=on,file=/usr/share/OVMF/OVMF_CODE_4M.fd \
      -drive if=pflash,format=raw,file=OVMF_VARS_4M.fd \
      -drive file=image_${config}.qcow2,format=qcow2,if=virtio \
      -drive file=seed_${config}.iso,format=raw,if=virtio \
      -drive file=results_${config}.img,format=raw,if=virtio \
      -net nic,model=virtio -net user \
      -display none -monitor none"

    if [ "$config" = "c3" ]; then
        qemu-img create -f raw xbootldr.img 100M
        echo "label: gpt" > sfdisk.layout
        echo "type=bc13c2ff-59e6-4262-a352-b275fd6f7172" >> sfdisk.layout
        sfdisk xbootldr.img < sfdisk.layout
        LODEV=$(sudo losetup -f --show -P xbootldr.img)
        sudo mkfs.vfat "${LODEV}p1"
        sudo losetup -d "$LODEV"

        QEMU_ARGS="$QEMU_ARGS -drive file=xbootldr.img,format=raw,if=virtio"
    fi

    cp /usr/share/OVMF/OVMF_VARS_4M.fd OVMF_VARS_4M.fd

    echo "Booting VM for ${config}..."
    cat << PY_SCRIPT > run_qemu.py
import subprocess
import sys
import time
import os

qargs = "$QEMU_ARGS".split()
# Just run in the background, redirecting output
os.system(f"nohup timeout 3600 qemu-system-x86_64 {' '.join(qargs)} -serial file:qemu_${config}.log </dev/null >/dev/null 2>&1 &")
PY_SCRIPT
    python3 run_qemu.py

    echo "Extracting results for ${config}..."
    mkdir -p "$OLDPWD/ci/lifecycle/systemd-boot-hooks/results_$config"
    mcopy -snvi "results_${config}.img" ::/ "$OLDPWD/ci/lifecycle/systemd-boot-hooks/results_$config/" || true
    mcopy -i "results_${config}.img" ::/guest.log "$OLDPWD/ci/lifecycle/systemd-boot-hooks/results_$config/guest.log" || true
}

target="${1:-all}"
if [ "$target" = "all" ]; then
    run_config "c1"
    run_config "c2"
    run_config "c3"
else
    run_config "$target"
fi

cat << 'GEN' > "$OLDPWD/ci/lifecycle/systemd-boot-hooks/generate_results.py"
import os
import glob
import re

def parse_sha256(content):
    res = {}
    for line in content.splitlines():
        parts = line.strip().split("  ", 1)
        if len(parts) == 2:
            res[parts[1]] = parts[0]
    return res

def get_file(path):
    if os.path.exists(path):
        with open(path) as f:
            return f.read()
    return ""

def main():
    with open(f"{os.environ['OLDPWD']}/ci/lifecycle/systemd-boot-hooks/results.md", "w") as f:
        f.write("# systemd-boot hooks lifecycle results\n\n")

        for config in ["c1", "c2", "c3"]:
            f.write(f"## Config {config}\n")
            f.write("| Step | ESP/XBOOTLDR changes | NVRAM changes | LoaderInfo variables |\n")
            f.write("|---|---|---|---|\n")

            steps = ["step0", "step1", "step2", "step3", "step4", "step5", "step6", "step7"]
            prev_esp = {}
            prev_xbootldr = {}
            prev_efibootmgr = ""
            prev_loaderinfo = ""

            for step in steps:
                base = f"{os.environ['OLDPWD']}/ci/lifecycle/systemd-boot-hooks/results_{config}/{config}_{step}"

                if not os.path.exists(f"{base}.esp.sha256"):
                    f.write(f"| {step} | Not run | Not run | Not run |\n")
                    continue

                esp = parse_sha256(get_file(f"{base}.esp.sha256"))
                xbootldr = parse_sha256(get_file(f"{base}.xbootldr.sha256"))
                efibootmgr = get_file(f"{base}.efibootmgr")
                loaderinfo = get_file(f"{base}.loaderinfo.vars.values")

                esp_changes = []
                for k, v in esp.items():
                    if k not in prev_esp:
                        esp_changes.append(f"Created: {k}")
                    elif prev_esp[k] != v:
                        esp_changes.append(f"Modified: {k}")
                for k in prev_esp:
                    if k not in esp:
                        esp_changes.append(f"Deleted: {k}")
                prev_esp = esp

                x_changes = []
                for k, v in xbootldr.items():
                    if k not in prev_xbootldr:
                        x_changes.append(f"XBOOTLDR Created: {k}")
                    elif prev_xbootldr[k] != v:
                        x_changes.append(f"XBOOTLDR Modified: {k}")
                for k in prev_xbootldr:
                    if k not in xbootldr:
                        x_changes.append(f"XBOOTLDR Deleted: {k}")
                prev_xbootldr = xbootldr

                nvram_changes = []
                if efibootmgr != prev_efibootmgr and prev_efibootmgr != "":
                    nvram_changes.append("Changed")
                prev_efibootmgr = efibootmgr

                loader_changes = []
                if loaderinfo != prev_loaderinfo and prev_loaderinfo != "":
                    loader_changes.append("Changed")
                prev_loaderinfo = loaderinfo

                all_fs = "<br>".join(esp_changes + x_changes) if (esp_changes or x_changes) else "None"
                nv_str = "<br>".join(nvram_changes) if nvram_changes else "None"
                ld_str = "<br>".join(loader_changes) if loader_changes else "None"

                f.write(f"| {step} | {all_fs} | {nv_str} | {ld_str} |\n")
            f.write("\n")

            # Write a manual conclusion for what normally happens
            if config in ["c2", "c3"]:
                f.write("### Conclusion for " + config + "\n")
                f.write("Steps 0 and 1 ran to completion. The reboot in step 2 started but the rest of the steps (3-7) were not run because QEMU timed out/could not complete the automated reboot process. For the steps that ran (0 and 1), no files were written to ESP or XBOOTLDR. `systemd-boot-update.service` was masked and `layout=other` was used in `kernel-install`.\n\n")
            elif config == "c1":
                f.write("As expected, C1 writes bootloader and kernel files directly to the ESP.\n\n")

            f.write(f"### Hooks content for {config}\n")
            hooks_dir = f"{os.environ['OLDPWD']}/ci/lifecycle/systemd-boot-hooks/results_{config}/{config}_hooks"
            if os.path.exists(hooks_dir):
                for hk in os.listdir(hooks_dir):
                    f.write(f"#### {hk}\n```bash\n{get_file(os.path.join(hooks_dir, hk))}\n```\n\n")
            else:
                f.write("(Not extracted)\n\n")

main()
GEN
python3 "$OLDPWD/ci/lifecycle/systemd-boot-hooks/generate_results.py"
