#!/bin/bash
set -euo pipefail

RUN_NAME="seabios"
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

CODE_FILE="/usr/share/seabios/bios-256k.bin"

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
    -machine "q35,accel=${ACCEL}"
    -m 1024
    -nographic
    -bios "${CODE_FILE}"
    -drive "if=virtio,format=qcow2,file=${OVERLAY}"
    -serial "file:${SERIAL_LOG}"
    -no-reboot
)

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
IMAGE_SHA512=$(sha512sum "$IMAGE_CACHE" | awk '{print $1}')
BOOT_TIME=$((END_TIME - START_TIME))

cat << EOT > "${OUT_DIR}/summary.txt"
Run: $RUN_NAME
QEMU version: $QEMU_VERSION
Accelerator: $ACCEL
Firmware code: $CODE_FILE (sha256: $CODE_SHA256)
Image sha512: $IMAGE_SHA512
Boot time (seconds): $BOOT_TIME
EOT

echo "Saved summary to ${OUT_DIR}/summary.txt"
