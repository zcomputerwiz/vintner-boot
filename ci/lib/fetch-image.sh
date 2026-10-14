#!/bin/bash
set -euo pipefail

PINNED_DATE="20261001-2618"
BASE_URL="https://cloud.debian.org/images/cloud/trixie/${PINNED_DATE}"
IMAGE_NAME="debian-13-genericcloud-amd64-${PINNED_DATE}.qcow2"
CACHE_DIR="ci/out/cache"
IMAGE_PATH="${CACHE_DIR}/${IMAGE_NAME}"
SUMS_FILE="SHA512SUMS"

mkdir -p "$CACHE_DIR"

if [ -f "${IMAGE_PATH}" ]; then
  echo "Image ${IMAGE_NAME} already cached."
else
  echo "Downloading ${IMAGE_NAME}..."
  curl -sSfL "${BASE_URL}/${IMAGE_NAME}" -o "${IMAGE_PATH}"
fi

echo "Downloading ${SUMS_FILE}..."
curl -sSfL "${BASE_URL}/${SUMS_FILE}" -o "${CACHE_DIR}/${SUMS_FILE}"

echo "Verifying SHA512SUMS..."
(cd "$CACHE_DIR" && grep "${IMAGE_NAME}" "${SUMS_FILE}" | sha512sum --check)

echo "Image path: ${IMAGE_PATH}"
