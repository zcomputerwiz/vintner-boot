// SPDX-License-Identifier: GPL-2.0-or-later

// Package menutimeout holds the menu timeout that a product sets (WO-0010 4.7):
// how many seconds the boot menu is shown at every boot before the default entry
// boots. 0, the installed setting, keeps the menu hidden and a key held at start
// opens it (docs/menu-key.md).
//
// UEFI keeps it in "timeout" of \loader\loader.conf on the ESP, which
// systemd-boot reads. BIOS keeps it in the grubenv variable VINTNER_TIMEOUT,
// which grub.cfg reads (firmware/bios/INTERFACE.md).
package menutimeout

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/nextboot"
)

// Max is the longest timeout accepted, in seconds; the same as next-boot's menu.
const Max = nextboot.MaxMenuTimeout

// GrubVar is the grubenv variable that carries the BIOS setting. It is absent
// when the menu is hidden.
const GrubVar = "VINTNER_TIMEOUT"

// override is the EFI variable that systemd-boot's menu keys (+ and -) write. It
// beats loader.conf, so a stale one would defeat the setting.
const override = "LoaderConfigTimeout"

// Valid parses s as a timeout: a whole number of seconds from 0 to Max.
func Valid(s string) (int, error) {
	n, err := strconv.Atoi(s)
	if err != nil || strconv.Itoa(n) != s || n < 0 || n > Max {
		return 0, fmt.Errorf("%q is not a number of seconds from 0 to %d", s, Max)
	}
	return n, nil
}

// directive reports whether line is loader.conf's "timeout" line, and its value.
func directive(line string) (string, bool) {
	f := strings.Fields(line)
	if len(f) == 0 || f[0] != "timeout" {
		return "", false
	}
	return strings.Join(f[1:], " "), true
}

// LoaderConf returns the text of loader.conf with its timeout set to seconds:
// the first "timeout" line is replaced, later ones dropped (systemd-boot uses the
// last, so a stale one would win), and the line is added when there is none. Every
// other line, comments included, stays as it was.
func LoaderConf(text string, seconds int) string {
	line := "timeout " + strconv.Itoa(seconds)
	var out []string
	done := false
	if text != "" {
		for _, l := range strings.Split(strings.TrimSuffix(text, "\n"), "\n") {
			if _, ok := directive(l); ok {
				if !done {
					out = append(out, line)
					done = true
				}
				continue
			}
			out = append(out, l)
		}
	}
	if !done {
		out = append(out, line)
	}
	return strings.Join(out, "\n") + "\n"
}

// ReadLoaderConf returns the timeout that loader.conf holds. ok is false when it
// has none, or one that is not a number of seconds ("menu-force" and the like,
// which the installer never writes).
func ReadLoaderConf(text string) (seconds int, ok bool) {
	for _, l := range strings.Split(text, "\n") {
		if v, found := directive(l); found {
			n, err := Valid(v)
			seconds, ok = n, err == nil // the last one wins, as in systemd-boot
		}
	}
	return seconds, ok
}

// SetBIOS stores the timeout in the grubenv at path. 0 removes the variable:
// the menu is hidden when it is absent.
func SetBIOS(path string, seconds int) error {
	env, err := grubenv.Read(path)
	if err != nil {
		return err
	}
	if seconds == 0 {
		env.Unset(GrubVar)
	} else {
		env.Set(GrubVar, strconv.Itoa(seconds))
	}
	return grubenv.Write(path, env)
}

// ReadBIOS returns the timeout that the grubenv at path holds, 0 when there is none.
func ReadBIOS(path string) (int, error) {
	env, err := grubenv.Read(path)
	if err != nil {
		return 0, err
	}
	v, ok := env.Get(GrubVar)
	if !ok || v == "" {
		return 0, nil
	}
	n, err := Valid(v)
	if err != nil {
		return 0, fmt.Errorf("%s in %s: %w", GrubVar, path, err)
	}
	return n, nil
}

// ClearOverride removes systemd-boot's own LoaderConfigTimeout, the one the menu keys
// set, and says whether there was one: an explicit setting of the product supersedes it.
func ClearOverride(efivarsDir string) (bool, error) {
	err := efivar.Delete(efivarsDir, override, efivar.LoaderInfo)
	if errors.Is(err, efivar.ErrNotFound) {
		return false, nil
	}
	return err == nil, err
}
