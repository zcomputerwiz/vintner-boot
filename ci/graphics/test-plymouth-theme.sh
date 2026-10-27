#!/usr/bin/env bash
# SPDX-License-Identifier: GPL-2.0-or-later
# test-plymouth-theme.sh - verify Vintner BGRT Plymouth theme configuration and assets (WO-0008)

set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
THEME_DIR="$REPO_ROOT/graphics/plymouth-theme"
THEME_FILE="$THEME_DIR/vintner-bgrt.plymouth"
GRUB_CFG="$REPO_ROOT/firmware/bios/grub.cfg"
INSTALL_FILE="$REPO_ROOT/debian/vintner-core.install"

PASS_COUNT=0
FAIL_COUNT=0

check() {
	local desc="$1"
	local result="$2" # 0 for pass, non-zero for fail
	local details="${3:-}"

	if [ "$result" -eq 0 ]; then
		printf "PASS: %s %s\n" "$desc" "$details"
		PASS_COUNT=$((PASS_COUNT + 1))
	else
		printf "FAIL: %s %s\n" "$desc" "$details"
		FAIL_COUNT=$((FAIL_COUNT + 1))
	fi
}

echo "=== Vintner BGRT Boot Logo & Plymouth Theme Verification (WO-0008) ==="

# Check 1: Theme file exists
if [ -f "$THEME_FILE" ]; then
	check "Theme file exists" 0 "$THEME_FILE"
else
	check "Theme file exists" 1 "missing $THEME_FILE"
fi

# Check 2: Theme definition uses two-step module and BGRT firmware background
if grep -q "ModuleName=two-step" "$THEME_FILE" && grep -q "UseFirmwareBackground=true" "$THEME_FILE"; then
	check "Theme specifies two-step module with UseFirmwareBackground=true" 0
else
	check "Theme specifies two-step module with UseFirmwareBackground=true" 1
fi

# Check 3: Theme specifies expected image directory
if grep -q "ImageDir=/usr/share/plymouth/themes/vintner-bgrt" "$THEME_FILE"; then
	check "Theme ImageDir targets /usr/share/plymouth/themes/vintner-bgrt" 0
else
	check "Theme ImageDir targets /usr/share/plymouth/themes/vintner-bgrt" 1
fi

# Check 4: Fallback boot logo exists and is valid PNG
FALLBACK_PNG="$THEME_DIR/bgrt-fallback.png"
if [ -f "$FALLBACK_PNG" ] && file "$FALLBACK_PNG" | grep -q "PNG image data"; then
	DIMENSIONS="$(file "$FALLBACK_PNG" | grep -o '[0-9]\+ x [0-9]\+')"
	check "bgrt-fallback.png is valid PNG" 0 "($DIMENSIONS)"
else
	check "bgrt-fallback.png is valid PNG" 1 "missing or invalid format"
fi

# Check 5: Watermark image exists and is valid PNG
WATERMARK_PNG="$THEME_DIR/watermark.png"
if [ -f "$WATERMARK_PNG" ] && file "$WATERMARK_PNG" | grep -q "PNG image data"; then
	check "watermark.png is valid PNG" 0
else
	check "watermark.png is valid PNG" 1
fi

# Check 6: Spinner animation frames exist (36 frames)
ANIM_FRAMES=0
for i in $(seq -w 1 36); do
	FRAME="$THEME_DIR/animation-00$i.png"
	if [ -f "$FRAME" ]; then
		ANIM_FRAMES=$((ANIM_FRAMES + 1))
	fi
done
if [ "$ANIM_FRAMES" -eq 36 ]; then
	check "Spinner animation frames complete" 0 "(36/36 frames present)"
else
	check "Spinner animation frames complete" 1 "($ANIM_FRAMES/36 frames found)"
fi

# Check 7: Authentication & password dialog assets present
DIALOG_ASSETS=("entry.png" "lock.png" "bullet.png" "capslock.png" "keyboard.png")
MISSING_DIALOG=0
for asset in "${DIALOG_ASSETS[@]}"; do
	if [ ! -f "$THEME_DIR/$asset" ]; then
		MISSING_DIALOG=$((MISSING_DIALOG + 1))
	fi
done
if [ "$MISSING_DIALOG" -eq 0 ]; then
	check "LUKS / Passphrase dialog assets present" 0 "(all 5 assets found)"
else
	check "LUKS / Passphrase dialog assets present" 1 "($MISSING_DIALOG missing)"
fi

# Check 8: Packaging rules install theme to /usr/share/plymouth/themes/vintner-bgrt/
if [ -f "$INSTALL_FILE" ] && grep -q "usr/share/plymouth/themes/vintner-bgrt" "$INSTALL_FILE"; then
	check "Debian packaging installs theme to /usr/share/plymouth/themes/vintner-bgrt" 0
else
	check "Debian packaging installs theme to /usr/share/plymouth/themes/vintner-bgrt" 1
fi

# Check 9: BIOS GRUB script syntax and linear framebuffer mode
if command -v grub-script-check >/dev/null 2>&1; then
	if grub-script-check "$GRUB_CFG" && grep -q "set gfxpayload=keep" "$GRUB_CFG"; then
		check "GRUB static configuration syntax valid with gfxpayload=keep" 0
	else
		check "GRUB static configuration syntax valid with gfxpayload=keep" 1
	fi
else
	if grep -q "set gfxpayload=keep" "$GRUB_CFG"; then
		check "GRUB static configuration has gfxpayload=keep (grub-script-check not installed)" 0
	else
		check "GRUB static configuration has gfxpayload=keep" 1
	fi
fi

# Check 10: Plymouth keyfile syntax validation
KEYFILE_STATUS=0
python3 -c "
import configparser
config = configparser.ConfigParser()
config.read('$THEME_FILE')
assert 'Plymouth Theme' in config, 'Missing Plymouth Theme section'
assert 'two-step' in config, 'Missing two-step section'
assert config['Plymouth Theme']['modulename'] == 'two-step', 'Wrong ModuleName'
assert config['boot-up']['usefirmwarebackground'] == 'true', 'Wrong UseFirmwareBackground'
" || KEYFILE_STATUS=$?
check "Theme keyfile syntax parsed cleanly by INI/keyfile parser" "$KEYFILE_STATUS"

echo ""
echo "Summary: $PASS_COUNT passed, $FAIL_COUNT failed."
if [ "$FAIL_COUNT" -gt 0 ]; then
	exit 1
fi
exit 0
