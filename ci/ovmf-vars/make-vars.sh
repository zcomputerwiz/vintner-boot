#!/bin/bash
set -euo pipefail

CERTS_DIR="ci/ovmf-vars/certs"
OUT_DIR="ci/out/ovmf-vars"
KEYS_DIR="$OUT_DIR/keys"

# Verify certificates
echo "Verifying certificates..."
(cd "$CERTS_DIR" && sha256sum -c SHA256SUMS)

# Locate OVMF_VARS_4M.fd
if [ -f /usr/share/OVMF/OVMF_VARS_4M.fd ]; then
    OVMF_VARS="/usr/share/OVMF/OVMF_VARS_4M.fd"
else
    echo "Error: OVMF_VARS_4M.fd not found in /usr/share/OVMF/."
    exit 1
fi
echo "Using OVMF vars template: $OVMF_VARS"

# Create output directories
mkdir -p "$KEYS_DIR"

# Generate test PK (RSA 2048, SHA-256, 10-year validity)
echo "Generating test PK..."
openssl req -new -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
    -subj "/CN=vintner-boot CI test PK" \
    -keyout "$KEYS_DIR/test-pk.key" -out "$OUT_DIR/test-pk.crt"

# Generate project test key in db (RSA 2048, SHA-256, 10-year validity)
echo "Generating project test key..."
openssl req -new -x509 -newkey rsa:2048 -sha256 -days 3650 -nodes \
    -subj "/CN=vintner-boot CI test db key" \
    -keyout "$KEYS_DIR/project-test.key" -out "$OUT_DIR/project-test.crt"

# Define GUIDs
PK_GUID="11111111-2222-3333-4444-555555555555"
KEK_GUID="66666666-7777-8888-9999-aaaaaaaaaaaa"
DB_GUID="bbbbbbbb-cccc-dddd-eeee-ffffffffffff"

echo "Building vars-setup-mode.fd..."
virt-fw-vars -i "$OVMF_VARS" -o "$OUT_DIR/vars-setup-mode.fd"

echo "Building vars-ms2011.fd..."
virt-fw-vars -i "$OVMF_VARS" -o "$OUT_DIR/vars-ms2011.fd" \
    --set-pk "$PK_GUID" "$OUT_DIR/test-pk.crt" \
    --add-kek "$KEK_GUID" "$CERTS_DIR/MicCorKEKCA2011_2011-06-24.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/MicWinProPCA2011_2011-10-19.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/MicCorUEFCA2011_2011-06-27.pem" \
    --secure-boot

echo "Building vars-ms2023.fd..."
virt-fw-vars -i "$OVMF_VARS" -o "$OUT_DIR/vars-ms2023.fd" \
    --set-pk "$PK_GUID" "$OUT_DIR/test-pk.crt" \
    --add-kek "$KEK_GUID" "$CERTS_DIR/microsoft corporation kek 2k ca 2023.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/windows uefi ca 2023.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/microsoft uefi ca 2023.pem" \
    --secure-boot

echo "Building vars-ms-both.fd..."
virt-fw-vars -i "$OVMF_VARS" -o "$OUT_DIR/vars-ms-both.fd" \
    --set-pk "$PK_GUID" "$OUT_DIR/test-pk.crt" \
    --add-kek "$KEK_GUID" "$CERTS_DIR/MicCorKEKCA2011_2011-06-24.pem" \
    --add-kek "$KEK_GUID" "$CERTS_DIR/microsoft corporation kek 2k ca 2023.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/MicWinProPCA2011_2011-10-19.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/MicCorUEFCA2011_2011-06-27.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/windows uefi ca 2023.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/microsoft uefi ca 2023.pem" \
    --secure-boot

echo "Building vars-project-test.fd..."
virt-fw-vars -i "$OVMF_VARS" -o "$OUT_DIR/vars-project-test.fd" \
    --set-pk "$PK_GUID" "$OUT_DIR/test-pk.crt" \
    --add-kek "$KEK_GUID" "$CERTS_DIR/MicCorKEKCA2011_2011-06-24.pem" \
    --add-kek "$KEK_GUID" "$CERTS_DIR/microsoft corporation kek 2k ca 2023.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/MicWinProPCA2011_2011-10-19.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/MicCorUEFCA2011_2011-06-27.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/windows uefi ca 2023.pem" \
    --add-db "$DB_GUID" "$CERTS_DIR/microsoft uefi ca 2023.pem" \
    --add-db "$DB_GUID" "$OUT_DIR/project-test.crt" \
    --secure-boot

# Validation function
validate_store() {
    local store_file="$1"
    local expected_pk="${2:-}"
    local expected_kek="${3:-}"
    local expected_db="${4:-}"

    local dump_file="${store_file%.fd}.txt"
    echo "Validating $store_file..."

    # We must run virt-fw-dump according to the prompt (or equivalent in virt-firmware version).
    # Since virt-fw-dump doesn't print cert subjects, we'll dump with virt-fw-dump first
    # and then use virt-fw-vars -p -v to verify the subjects, appending both to the dump file.
    virt-fw-dump -i "$store_file" > "$dump_file"
    virt-fw-vars -i "$store_file" -p -v >> "$dump_file" 2>/dev/null || true

    if [ -n "$expected_pk" ]; then
        if ! grep -q "$expected_pk" "$dump_file"; then
            echo "Error: Missing PK '$expected_pk' in $store_file"
            exit 1
        fi
    fi

    if [ -n "$expected_kek" ]; then
        IFS='|' read -r -a kek_array <<< "$expected_kek"
        for kek in "${kek_array[@]}"; do
            if ! grep -q "$kek" "$dump_file"; then
                echo "Error: Missing KEK '$kek' in $store_file"
                exit 1
            fi
        done
    fi

    if [ -n "$expected_db" ]; then
        IFS='|' read -r -a db_array <<< "$expected_db"
        for db in "${db_array[@]}"; do
            if ! grep -q "$db" "$dump_file"; then
                echo "Error: Missing db entry '$db' in $store_file"
                exit 1
            fi
        done
    fi
}

# Check that unexpected certificates are NOT present
check_unexpected() {
    local dump_file="$1"
    local unexpected="$2"
    if grep -q "$unexpected" "$dump_file"; then
        echo "Error: Unexpected certificate '$unexpected' found in $dump_file"
        exit 1
    fi
}

echo "Validating stores..."
validate_store "$OUT_DIR/vars-setup-mode.fd" "" "" ""
validate_store "$OUT_DIR/vars-ms2011.fd" "vintner-boot CI test PK" "Microsoft Corporation KEK CA 2011" "Microsoft Windows Production PCA 2011|Microsoft Corporation UEFI CA 2011"
validate_store "$OUT_DIR/vars-ms2023.fd" "vintner-boot CI test PK" "Microsoft Corporation KEK 2K CA 2023" "Windows UEFI CA 2023|Microsoft UEFI CA 2023"
validate_store "$OUT_DIR/vars-ms-both.fd" "vintner-boot CI test PK" "Microsoft Corporation KEK CA 2011|Microsoft Corporation KEK 2K CA 2023" "Microsoft Windows Production PCA 2011|Microsoft Corporation UEFI CA 2011|Windows UEFI CA 2023|Microsoft UEFI CA 2023"
validate_store "$OUT_DIR/vars-project-test.fd" "vintner-boot CI test PK" "Microsoft Corporation KEK CA 2011|Microsoft Corporation KEK 2K CA 2023" "Microsoft Windows Production PCA 2011|Microsoft Corporation UEFI CA 2011|Windows UEFI CA 2023|Microsoft UEFI CA 2023|vintner-boot CI test db key"

echo "Checking for unexpected certificates..."
check_unexpected "$OUT_DIR/vars-setup-mode.txt" "vintner-boot CI test PK"
check_unexpected "$OUT_DIR/vars-ms2011.txt" "Microsoft Corporation KEK 2K CA 2023"
check_unexpected "$OUT_DIR/vars-ms2011.txt" "Microsoft UEFI CA 2023"
check_unexpected "$OUT_DIR/vars-ms2023.txt" "Microsoft Corporation KEK CA 2011"
check_unexpected "$OUT_DIR/vars-ms2023.txt" "Microsoft Corporation UEFI CA 2011"
check_unexpected "$OUT_DIR/vars-ms-both.txt" "vintner-boot CI test db key"
check_unexpected "$OUT_DIR/vars-ms2011.txt" "vintner-boot CI test db key"
check_unexpected "$OUT_DIR/vars-ms2023.txt" "vintner-boot CI test db key"

echo "All stores built and validated successfully."
