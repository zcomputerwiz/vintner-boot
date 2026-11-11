// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/zcomputerwiz/vintner-boot/src/internal/atomicfile"
	"github.com/zcomputerwiz/vintner-boot/src/internal/bootstorage"
	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/menutimeout"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

// Exit codes of vintner menu-timeout (docs/menu-timeout.md).
const (
	menuTimeoutOK     = 0
	menuTimeoutFailed = 1 // an I/O or state error
	menuTimeoutUsage  = 2
	menuTimeoutNone   = 3 // not a vintner installation: no valid manifest
)

func cmdMenuTimeout(args []string) {
	osExit(runMenuTimeout(args, os.Stdout, os.Stderr))
}

// menuTimeoutResult is the --json output, on success and on failure.
type menuTimeoutResult struct {
	OK bool `json:"ok"`
	// Firmware is what the installation supports: uefi, bios or both.
	Firmware string `json:"firmware,omitempty"`
	// Timeout is the setting, in seconds; 0 keeps the menu hidden. When the
	// firmwares disagree it is the one of the loader that booted this system.
	Timeout *int `json:"timeout,omitempty"`
	UEFI    *int `json:"uefi,omitempty"`
	BIOS    *int `json:"bios,omitempty"`
	// ClearedOverride is set when a LoaderConfigTimeout that the menu keys had
	// written was removed, because it beats loader.conf.
	ClearedOverride bool   `json:"cleared_override,omitempty"`
	Code            string `json:"code,omitempty"`
	Error           string `json:"error,omitempty"`
}

// runMenuTimeout implements vintner menu-timeout [<seconds>] (WO-0010 4.7): it shows
// or sets how long the boot menu is displayed at every boot. 0 keeps it hidden.
func runMenuTimeout(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner menu-timeout", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount the partition named by vintner.bootstorage= on demand)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")
	espDir := fs.String("esp", "", "the mounted ESP (default: mount the manifest's ESP on demand)")
	efivarsDir := fs.String("efivars", efivar.DefaultDir, "efivars directory")
	asJSON := fs.Bool("json", false, "print the result as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: vintner menu-timeout [--json] [<seconds>]")
		fs.PrintDefaults()
	}
	// Flags may come before or after the number.
	if err := fs.Parse(args); err != nil {
		return menuTimeoutUsage
	}
	operands := fs.Args()
	if len(operands) > 0 {
		if err := fs.Parse(operands[1:]); err != nil {
			return menuTimeoutUsage
		}
		operands = append([]string{operands[0]}, fs.Args()...)
	}

	fail := func(code int, kind string, err error) int {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(menuTimeoutResult{Code: kind, Error: err.Error()})
		} else {
			fmt.Fprintf(stderr, "vintner menu-timeout: %v\n", err)
		}
		return code
	}
	if len(operands) > 1 {
		return fail(menuTimeoutUsage, "usage", fmt.Errorf("unexpected argument %q", operands[1]))
	}
	set := len(operands) == 1
	seconds := 0
	if set {
		var err error
		if seconds, err = menutimeout.Valid(operands[0]); err != nil {
			return fail(menuTimeoutUsage, "usage", err)
		}
	}

	if set {
		// A writer: no publish, and no other writer, while the settings change.
		lock, err := publish.AcquireLock(publish.DefaultLockPath(*bootStorage))
		if err != nil {
			return fail(menuTimeoutFailed, "failed", err)
		}
		defer lock.Close()
	}
	dir, err := mountBootStorage(*bootStorage, set)
	if err != nil {
		return fail(menuTimeoutFailed, "failed", err)
	}
	sDir := *stateDir
	if sDir == "" {
		sDir = filepath.Join(dir, "vintner", "state")
	}
	m, err := manifest.Load(sDir)
	if err != nil {
		if errors.Is(err, manifest.ErrNoValidCopy) {
			return fail(menuTimeoutNone, "not-vintner", fmt.Errorf("no valid manifest in %s: this is not a vintner installation", sDir))
		}
		return fail(menuTimeoutFailed, "failed", fmt.Errorf("failed to load manifest: %w", err))
	}
	useUEFI := m.Firmware == "uefi" || m.Firmware == "both"
	useBIOS := m.Firmware == "bios" || m.Firmware == "both"
	grubenvPath := filepath.Join(dir, "grub", "grubenv")

	res := menuTimeoutResult{OK: true, Firmware: m.Firmware}
	var loaderConf string
	if useUEFI {
		esp, unmount, err := espFor(m, *espDir, set)
		if err != nil {
			return fail(menuTimeoutFailed, "failed", err)
		}
		defer unmount()
		loaderConf = filepath.Join(esp, "loader", "loader.conf")
		data, err := os.ReadFile(loaderConf)
		if err != nil {
			// Without it systemd-boot would lose "default vintner-*": never create one here.
			return fail(menuTimeoutFailed, "failed", fmt.Errorf("the ESP's loader.conf cannot be read: %w", err))
		}
		if set {
			if err := atomicfile.Write(loaderConf, []byte(menutimeout.LoaderConf(string(data), seconds)), 0o644); err != nil {
				return fail(menuTimeoutFailed, "failed", fmt.Errorf("writing loader.conf: %w", err))
			}
			data, err = os.ReadFile(loaderConf)
			if err != nil {
				return fail(menuTimeoutFailed, "failed", err)
			}
		}
		n, ok := menutimeout.ReadLoaderConf(string(data))
		if !ok {
			return fail(menuTimeoutFailed, "failed", fmt.Errorf("loader.conf holds no timeout that is a number of seconds"))
		}
		res.UEFI = &n
	}
	if useBIOS {
		if set {
			if err := menutimeout.SetBIOS(grubenvPath, seconds); err != nil {
				return fail(menuTimeoutFailed, "failed", fmt.Errorf("writing the grubenv: %w", err))
			}
		}
		n, err := menutimeout.ReadBIOS(grubenvPath)
		if err != nil {
			return fail(menuTimeoutFailed, "failed", err)
		}
		res.BIOS = &n
	}
	if set && useUEFI {
		if info, err := os.Stat(*efivarsDir); err == nil && info.IsDir() {
			res.ClearedOverride, err = menutimeout.ClearOverride(*efivarsDir)
			if err != nil {
				return fail(menuTimeoutFailed, "failed", fmt.Errorf("clearing LoaderConfigTimeout: %w", err))
			}
		}
	}
	// The one number: the loader that booted this system, else the one there is.
	info, statErr := os.Stat(*efivarsDir)
	bootedUEFI := statErr == nil && info.IsDir()
	switch {
	case bootedUEFI && res.UEFI != nil:
		res.Timeout = res.UEFI
	case res.BIOS != nil:
		res.Timeout = res.BIOS
	default:
		res.Timeout = res.UEFI
	}

	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(res)
		return menuTimeoutOK
	}
	if *res.Timeout == 0 {
		fmt.Fprintln(stdout, "menu timeout: 0 (the menu is hidden; a key held at start opens it)")
	} else {
		fmt.Fprintf(stdout, "menu timeout: %d seconds\n", *res.Timeout)
	}
	if res.UEFI != nil && res.BIOS != nil && *res.UEFI != *res.BIOS {
		fmt.Fprintf(stdout, "  UEFI %d, BIOS %d\n", *res.UEFI, *res.BIOS)
	}
	if res.ClearedOverride {
		fmt.Fprintln(stdout, "  removed the LoaderConfigTimeout that the menu keys had set")
	}
	return menuTimeoutOK
}

// espFor returns the directory of the ESP: the one named, or the manifest's,
// mounted on demand (read-write only when the caller writes), and the function
// that releases it.
func espFor(m *manifest.Manifest, named string, rw bool) (string, func(), error) {
	if named != "" {
		return named, func() {}, nil
	}
	if m.ESP == nil {
		return "", nil, errors.New("the manifest records no ESP: name the mounted one with --esp")
	}
	dev, err := bootstorage.ESPDevice(m.ESP.PartUUID)
	if err != nil {
		return "", nil, err
	}
	mount := bootstorage.MountESP
	if rw {
		mount = bootstorage.MountESPWritable
	}
	unmount, err := mount(dev)
	if err != nil {
		return "", nil, err
	}
	return bootstorage.ESPMountPoint, func() {
		if err := unmount(); err != nil {
			fmt.Fprintf(os.Stderr, "vintner: unmount %s: %v\n", bootstorage.ESPMountPoint, err)
		}
	}, nil
}
