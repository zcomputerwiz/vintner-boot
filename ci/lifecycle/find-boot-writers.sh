#!/bin/bash
set -euo pipefail

MIRROR="https://deb.debian.org/debian"
SUITE="trixie"
COMPONENTS=("main" "contrib" "non-free" "non-free-firmware")
CACHE_DIR="ci/out/cache/lifecycle"

mkdir -p "$CACHE_DIR"

# 1. Download Release and Verify
RELEASE_FILE="$CACHE_DIR/Release"
if [ ! -f "$RELEASE_FILE" ]; then
    echo "Downloading Release..." >&2
    curl -sL "$MIRROR/dists/$SUITE/Release" -o "$RELEASE_FILE"
fi

for comp in "${COMPONENTS[@]}"; do
    contents_file="$CACHE_DIR/Contents-${comp}-amd64.gz"
    packages_file="$CACHE_DIR/Packages-${comp}-amd64.xz"

    contents_path="${comp}/Contents-amd64.gz"
    packages_path="${comp}/binary-amd64/Packages.xz"

    if [ ! -f "$contents_file" ]; then
        echo "Downloading $contents_path..." >&2
        curl -sL "$MIRROR/dists/$SUITE/$contents_path" -o "$contents_file"

        # Verify checksum
        expected_hash=$(awk -v path="$contents_path" '
            /^SHA256:/ { in_sha256 = 1; next }
            /^[A-Za-z0-9]+:/ && !/^SHA256:/ { in_sha256 = 0 }
            in_sha256 && $3 == path { print $1; _ex = 1 }
        ' "$RELEASE_FILE")
        actual_hash=$(sha256sum "$contents_file" | awk '{print $1}')
        if [ "$expected_hash" != "$actual_hash" ]; then
            echo "Checksum mismatch for $contents_path: expected $expected_hash, got $actual_hash" >&2
            false
        fi
    fi

    if [ ! -f "$packages_file" ]; then
        echo "Downloading $packages_path..." >&2
        curl -sL "$MIRROR/dists/$SUITE/$packages_path" -o "$packages_file"

        # Verify checksum
        expected_hash=$(awk -v path="$packages_path" '
            /^SHA256:/ { in_sha256 = 1; next }
            /^[A-Za-z0-9]+:/ && !/^SHA256:/ { in_sha256 = 0 }
            in_sha256 && $3 == path { print $1; _ex = 1 }
        ' "$RELEASE_FILE")
        actual_hash=$(sha256sum "$packages_file" | awk '{print $1}')
        if [ "$expected_hash" != "$actual_hash" ]; then
            echo "Checksum mismatch for $packages_path: expected $expected_hash, got $actual_hash" >&2
            false
        fi
    fi
done

# 2. Build Candidate Set
RULE_A_PKGS="$CACHE_DIR/rule_a.txt"
RULE_B_PKGS="$CACHE_DIR/rule_b.txt"
ALL_CANDIDATES="$CACHE_DIR/candidates.txt"
PKGS_METADATA="$CACHE_DIR/pkgs_meta.txt"

# Rule A
true > "$RULE_A_PKGS"
for comp in "${COMPONENTS[@]}"; do
    contents_file="$CACHE_DIR/Contents-${comp}-amd64.gz"
    zcat "$contents_file" | awk '
      $1 ~ /^etc\/kernel\/(postinst\.d|postrm\.d|preinst\.d|prerm\.d|install\.d|install\.conf)/ ||
      $1 ~ /^etc\/initramfs\/post-update\.d\// ||
      $1 ~ /^usr\/lib\/kernel\/install\.d\// ||
      $1 ~ /^usr\/share\/initramfs-tools\/hooks\// ||
      $1 ~ /^etc\/grub\.d\// {
        n = split($2, pkgs, ",")
        for (i = 1; i <= n; i++) {
            split(pkgs[i], p, "/")
            print p[length(p)]
        }
      }
    ' >> "$RULE_A_PKGS"
done
sort -u "$RULE_A_PKGS" -o "$RULE_A_PKGS"

# Rule B/C Dependencies
B_DEPS="grub-common|grub2-common|grub-pc|grub-pc-bin|grub-efi-amd64|grub-efi-amd64-bin|grub-efi-amd64-signed|shim-signed|efibootmgr|systemd-boot|systemd-boot-efi|systemd-ukify|dracut|initramfs-tools"
true > "$RULE_B_PKGS"
true > "$PKGS_METADATA"
for comp in "${COMPONENTS[@]}"; do
    packages_file="$CACHE_DIR/Packages-${comp}-amd64.xz"
    xzcat "$packages_file" | awk -v b_deps="$B_DEPS" '
        BEGIN { split(b_deps, deps, "|"); for (d in deps) dep_map[deps[d]] = 1 }
        /^Package: / { pkg = $2 }
        /^Version: / { ver = $2 }
        /^Filename: / { file = $2 }
        /^Depends: / || /^Pre-Depends: / || /^Recommends: / { in_deps = 1 }
        /^[a-zA-Z0-9-]+: / { if ($1 !~ /^(Depends|Pre-Depends|Recommends):/) in_deps = 0 }
        in_deps {
            # parse dependencies
            line = $0
            for (d in dep_map) {
                if (line ~ "(^|[ ,])" d "([ ,]|$)") {
                    matched = 1
                }
            }
        }
        /^$/ {
            if (pkg != "" && ver != "" && file != "") {
                print pkg "\t" ver "\t" file >> "'"$PKGS_METADATA"'"
                if (matched || (pkg in dep_map)) {
                    print pkg >> "'"$RULE_B_PKGS"'"
                }
            }
            pkg = ""; ver = ""; file = ""; matched = 0
        }
    '
done
sort -u "$RULE_B_PKGS" -o "$RULE_B_PKGS"

sort -u "$RULE_A_PKGS" "$RULE_B_PKGS" > "$ALL_CANDIDATES"

# 3. and 4. Download, extract and search
TSV_FILE="ci/lifecycle/boot-writers-trixie.tsv"
rm -f "$TSV_FILE.tmp"
true > "$TSV_FILE"
echo -e "package\tversion\tmember\tcommand\tline\texcerpt" >> "$TSV_FILE"

COMMANDS="grub-install|update-grub|grub-mkconfig|grub-set-default|grub-reboot|grub-editenv|bootctl|efibootmgr|kernel-install|update-initramfs|dracut|ukify"

while read -r pkg; do
    # Get metadata
    meta=$(awk -F'\t' -v p="$pkg" '$1 == p { print $0 }' "$PKGS_METADATA" | head -n 1)
    if [ -z "$meta" ]; then
        continue
    fi
    ver=$(echo "$meta" | cut -f2)
    file=$(echo "$meta" | cut -f3)

    is_rule_a=0
    if grep -qFx "$pkg" "$RULE_A_PKGS"; then
        is_rule_a=1
    fi

    # Create package cache dir
    pkg_dir="$CACHE_DIR/extracted/${pkg}_${ver}"
    if [ ! -d "$pkg_dir" ]; then
        mkdir -p "$pkg_dir"
        echo "Processing $pkg $ver..." >&2

        if [ "$is_rule_a" -eq 1 ]; then
            # Download full deb
            deb_file="$CACHE_DIR/${pkg}_${ver}.deb"
            if [ ! -f "$deb_file" ]; then
                curl -sL "$MIRROR/$file" -o "$deb_file"
            fi

            # Extract control
            mkdir -p "$pkg_dir/control"
            dpkg-deb -e "$deb_file" "$pkg_dir/control"

            # Extract data
            mkdir -p "$pkg_dir/data"
            dpkg-deb -x "$deb_file" "$pkg_dir/data"
        else
            # Download only control
            # ar header is 8 bytes magic + 60 bytes member 1 + 60 bytes member 2
            hdr=$(curl -sL -r 0-131 "$MIRROR/$file")

            # member 2 starts at 72, length 60
            m2_hdr="${hdr:72:60}"
            m2_size=$(echo "${m2_hdr:48:10}" | tr -d ' ')

            if [ -z "$m2_size" ]; then
                echo "Failed to parse ar header for $pkg" >&2
                continue
            fi

            start=132
            end=$(( start + m2_size - 1 ))

            mkdir -p "$pkg_dir/control"
            curl -sL -r "$start-$end" "$MIRROR/$file" -o "$pkg_dir/control_tarball"

            tar -xf "$pkg_dir/control_tarball" -C "$pkg_dir/control" 2>/dev/null || true
            rm -f "$pkg_dir/control_tarball"
        fi
    fi

    # Search extracted files
    search_files=()
    for f in preinst postinst prerm postrm config triggers; do
        if [ -f "$pkg_dir/control/$f" ]; then
            search_files+=("$pkg_dir/control/$f")
        fi
    done

    if [ "$is_rule_a" -eq 1 ] && [ -d "$pkg_dir/data" ]; then
        # Check rule A paths
        while read -r path; do
            if [ -f "$pkg_dir/data/$path" ]; then
                search_files+=("$pkg_dir/data/$path")
            fi
        done < <(find "$pkg_dir/data/etc/kernel/" "$pkg_dir/data/etc/initramfs/post-update.d/" "$pkg_dir/data/usr/lib/kernel/install.d/" "$pkg_dir/data/usr/share/initramfs-tools/hooks/" "$pkg_dir/data/etc/grub.d/" -type f 2>/dev/null | sed "s|^$pkg_dir/data/||" | sort)
    fi

    for f in "${search_files[@]}"; do
        # Member name logic
        if [[ "$f" == "$pkg_dir"/control/* ]]; then
            member_name="${f#"$pkg_dir"/control/}"
        else
            member_name="${f#"$pkg_dir"/data/}"
        fi

        # Triggers file
        if [ "$member_name" == "triggers" ]; then
            awk -v pkg="$pkg" -v ver="$ver" -v member="$member_name" '
              $1 == "interest" || $1 == "interest-await" || $1 == "interest-noawait" || $1 == "activate" || $1 == "activate-await" || $1 == "activate-noawait" {
                if ($2 ~ /^(grub-install|update-grub|grub-mkconfig|grub-set-default|grub-reboot|grub-editenv|bootctl|efibootmgr|kernel-install|update-initramfs|dracut|ukify)$/) {
                    line = $0; gsub(/\t/, " ", line)
                    print pkg "\t" ver "\t" member "\t" $2 "\t" NR "\t" line
                }
              }
            ' "$f" >> "$TSV_FILE".tmp
            continue
        fi

        # grep for commands
        awk -v cmds="$COMMANDS" -v pkg="$pkg" -v ver="$ver" -v member="$member_name" '
            BEGIN { split(cmds, c, "|"); for(i in c) match_cmd[c[i]]=1 }
            {
                for (cmd in match_cmd) {
                    if (index($0, cmd) > 0) {
                        if ($0 ~ "(^|[^a-zA-Z0-9_-])" cmd "([^a-zA-Z0-9_-]|$)") {
                            line = $0; gsub(/\t/, " ", line)
                            print pkg "\t" ver "\t" member "\t" cmd "\t" NR "\t" line
                        }
                    }
                }
            }
        ' "$f" >> "$TSV_FILE".tmp || true
    done
done < "$ALL_CANDIDATES"

if [ -f "$TSV_FILE".tmp ]; then
    sort -u -t$'\t' -k1,1 -k3,3 -k5,5n "$TSV_FILE".tmp >> "$TSV_FILE"
    rm -f "$TSV_FILE".tmp
fi

echo "Done writing TSV." >&2
