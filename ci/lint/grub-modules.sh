#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Checks that firmware/bios/grub.cfg loads no module that core.img lacks:
# every "insmod X" must name a module in firmware/bios/core-modules.txt, and
# the module list in firmware/bios/INTERFACE.md must equal that file.
set -euo pipefail
cd "$(dirname "$0")/../.."

cfg=firmware/bios/grub.cfg
list=firmware/bios/core-modules.txt
doc=firmware/bios/INTERFACE.md
fail=0

read -r -a mods < "$list"

while read -r m; do
	found=0
	for x in "${mods[@]}"; do [ "$x" = "$m" ] && found=1; done
	if [ "$found" = 0 ]; then
		echo "FAIL: $cfg runs 'insmod $m', which core.img does not contain ($list)"
		fail=1
	fi
done < <(sed -n 's/^[[:space:]]*insmod[[:space:]]\+\([A-Za-z0-9_]\+\).*/\1/p' "$cfg")

doclist=$(grep -m1 -E '^  `biosdisk ' "$doc" | tr -d '`' | sed 's/^ *//')
if [ "$doclist" != "${mods[*]}" ]; then
	echo "FAIL: module list in $doc differs from $list"
	echo "  doc:  $doclist"
	echo "  file: ${mods[*]}"
	fail=1
fi

[ "$fail" = 0 ] && echo "PASS: grub.cfg modules are all embedded in core.img"
exit "$fail"
