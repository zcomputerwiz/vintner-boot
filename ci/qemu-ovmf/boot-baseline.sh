#!/bin/bash
set -euo pipefail

SECURE_BOOT="off"

while [[ "$#" -gt 0 ]]; do
    case $1 in
        --secure-boot) SECURE_BOOT="$2"; shift ;;
        *) echo "Unknown parameter passed: $1"; exit 1 ;;
    esac
    shift
done

if [[ "$SECURE_BOOT" != "on" && "$SECURE_BOOT" != "off" ]]; then
    echo "--secure-boot must be 'on' or 'off'"
    exit 1
fi

RUN_NAME="ovmf-sb-${SECURE_BOOT}"
OUT_DIR="ci/out/${RUN_NAME}"
mkdir -p "$OUT_DIR"

CACHE_DIR="$(pwd)/ci/out/cache"
IMAGE_CACHE="${CACHE_DIR}/debian-13-genericcloud-amd64-20261001-2618.qcow2"

if [ ! -f "$IMAGE_CACHE" ]; then
    echo "Image not found, running fetch-image.sh..."
    ./ci/lib/fetch-image.sh
fi

OVERLAY="${OUT_DIR}/overlay.qcow2"
qemu-img create -f qcow2 -b "${IMAGE_CACHE}" -F qcow2 "$OVERLAY" >/dev/null

VARS_FILE="${OUT_DIR}/vars.fd"
if [ "$SECURE_BOOT" = "on" ]; then
    CODE_FILE="/usr/share/OVMF/OVMF_CODE_4M.secboot.fd"
    cp "/usr/share/OVMF/OVMF_VARS_4M.ms.fd" "$VARS_FILE"
else
    CODE_FILE="/usr/share/OVMF/OVMF_CODE_4M.fd"
    cp "/usr/share/OVMF/OVMF_VARS_4M.fd" "$VARS_FILE"
fi

chmod +w "$VARS_FILE"

ACCEL="tcg"
TIMEOUT=300
if [ -w /dev/kvm ]; then
    ACCEL="kvm"
    TIMEOUT=60
fi

SERIAL_LOG="${OUT_DIR}/serial.log"
true > "$SERIAL_LOG"

QEMU_CMD=(
    qemu-system-x86_64
    -machine "q35,accel=${ACCEL},smm=on"
    -m 1024
    -nographic
    -drive "if=pflash,format=raw,unit=0,file=${CODE_FILE},readonly=on"
    -drive "if=pflash,format=raw,unit=1,file=${VARS_FILE}"
    -drive "if=virtio,format=qcow2,file=${OVERLAY}"
    -serial "file:${SERIAL_LOG}"
    -no-reboot
)

if [ "$SECURE_BOOT" = "on" ]; then
    QEMU_CMD+=("-global" "driver=cfi.pflash01,property=secure,value=on")
fi

echo "Running QEMU ($RUN_NAME, $ACCEL)..."
START_TIME=$(date +%s)

set +e
timeout "${TIMEOUT}" "${QEMU_CMD[@]}" &
QEMU_PID=$!

SUCCESS=0
while sleep 2; do
    if grep -qE "login:|Cloud-init v\. .* finished" "$SERIAL_LOG"; then
        SUCCESS=1
        break
    fi
    if ! kill -0 "$QEMU_PID" 2>/dev/null; then
        echo "QEMU died unexpectedly!"
        break
    fi
done

if [ "$SUCCESS" -eq 1 ]; then
    echo "Boot success detected."
    # send SIGTERM
    kill "$QEMU_PID" 2>/dev/null || true
    wait "$QEMU_PID" 2>/dev/null || true
else
    echo "Boot failed or timed out. Last 20 lines of serial log:"
    tail -n 20 "$SERIAL_LOG"
    kill -9 "$QEMU_PID" 2>/dev/null || true
    wait "$QEMU_PID" 2>/dev/null || true
    exit 1
fi
set -e
END_TIME=$(date +%s)

QEMU_VERSION=$(qemu-system-x86_64 --version | head -n1)
CODE_SHA256=$(sha256sum "$CODE_FILE" | awk '{print $1}')
VARS_SHA256=$(sha256sum "$VARS_FILE" | awk '{print $1}')
IMAGE_SHA512=$(sha512sum "$IMAGE_CACHE" | awk '{print $1}')
BOOT_TIME=$((END_TIME - START_TIME))

cat << EOT > "${OUT_DIR}/summary.txt"
Run: $RUN_NAME
QEMU version: $QEMU_VERSION
Accelerator: $ACCEL
Firmware code: $CODE_FILE (sha256: $CODE_SHA256)
Firmware vars: $VARS_FILE (sha256: $VARS_SHA256)
Image sha512: $IMAGE_SHA512
Boot time (seconds): $BOOT_TIME
EOT

echo "Saved summary to ${OUT_DIR}/summary.txt"
