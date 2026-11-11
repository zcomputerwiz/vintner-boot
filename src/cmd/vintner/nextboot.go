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
	"strconv"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/nextboot"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
	"github.com/zcomputerwiz/vintner-boot/src/internal/variants"
)

// Exit codes of vintner next-boot (docs/next-boot.md).
const (
	nextBootOK          = 0
	nextBootFailed      = 1 // an I/O or state error
	nextBootUsage       = 2
	nextBootUnavailable = 3 // the request cannot be honoured: no such generation, no previous good, ...
)

func cmdNextBoot(args []string) {
	osExit(runNextBoot(args, os.Stdout, os.Stderr))
}

// nextBootResult is the --json output, on success and on failure.
type nextBootResult struct {
	OK         bool   `json:"ok"`
	Target     string `json:"target,omitempty"`
	Firmware   string `json:"firmware,omitempty"`
	Generation *int   `json:"generation,omitempty"`
	Variant    string `json:"variant,omitempty"`
	Timeout    *int   `json:"timeout,omitempty"`
	Code       string `json:"code,omitempty"`
	Error      string `json:"error,omitempty"`
}

// runNextBoot implements vintner next-boot: it sets what the next boot does,
// once, and leaves the restart to the caller (WO-0010 4.4).
func runNextBoot(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner next-boot", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount the partition named by vintner.bootstorage= on demand)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")
	efivarsDir := fs.String("efivars", efivar.DefaultDir, "efivars directory")
	timeout := fs.Int("timeout", nextboot.DefaultMenuTimeout, "menu: seconds to wait before the default entry boots")
	genFlag := fs.Int("generation", -1, "variant: the generation whose variant to boot, even if it has no tries left (default: the one that would boot next)")
	asJSON := fs.Bool("json", false, "print the result as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: vintner next-boot [--json] recovery | previous-good | generation <n> | variant [--generation <n>] <id> | menu [--timeout <s>] | clear")
		fs.PrintDefaults()
	}
	// Flags may come before or after the target.
	if err := fs.Parse(args); err != nil {
		return nextBootUsage
	}
	rest := fs.Args()
	var target string
	var operands []string
	if len(rest) > 0 {
		target, operands = rest[0], rest[1:]
		// Flags after the target.
		if err := fs.Parse(operands); err != nil {
			return nextBootUsage
		}
		// Flags between and after the operands too: variant safe --generation 3.
		var all []string
		for rest := fs.Args(); len(rest) > 0; rest = fs.Args() {
			all = append(all, rest[0])
			if err := fs.Parse(rest[1:]); err != nil {
				return nextBootUsage
			}
		}
		operands = all
	}

	fail := func(code int, kind, msg string) int {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(nextBootResult{Code: kind, Error: msg})
		} else {
			fmt.Fprintf(stderr, "vintner next-boot: %s\n", msg)
		}
		return code
	}
	usage := func(msg string) int { return fail(nextBootUsage, "usage", msg) }

	t := nextboot.Target{}
	wantArgs := 0
	switch target {
	case "recovery":
		t.Kind = nextboot.Recovery
	case "previous-good":
		t.Kind = nextboot.Generation
	case "generation":
		t.Kind = nextboot.Generation
		wantArgs = 1
	case "variant":
		t.Kind = nextboot.Variant
		wantArgs = 1
	case "menu":
		t.Kind = nextboot.Menu
	case "clear":
		t.Kind = nextboot.Clear
	case "":
		return usage("a target is required: recovery, previous-good, generation <n>, variant <id>, menu or clear")
	default:
		return usage(fmt.Sprintf("unknown target %q", target))
	}
	if *genFlag != -1 && target != "variant" {
		return usage("--generation goes only with the variant target")
	}
	if len(operands) != wantArgs {
		return usage(fmt.Sprintf("%s takes %d argument(s), not %d", target, wantArgs, len(operands)))
	}
	switch target {
	case "generation":
		g, err := strconv.Atoi(operands[0])
		if err != nil || g < 0 || strconv.Itoa(g) != operands[0] {
			return usage(fmt.Sprintf("%q is not a generation number", operands[0]))
		}
		t.Generation = g
	case "variant":
		if !variants.ValidID(operands[0]) {
			return usage(fmt.Sprintf("%q is not a variant ID (lower case letters, digits and _, starting with a letter)", operands[0]))
		}
		t.Variant = operands[0]
		if *genFlag < -1 {
			return usage(fmt.Sprintf("--generation %d is not a generation number", *genFlag))
		}
	case "menu":
		if *timeout < 1 || *timeout > nextboot.MaxMenuTimeout {
			return usage(fmt.Sprintf("--timeout must be 1 to %d seconds", nextboot.MaxMenuTimeout))
		}
		t.Timeout = *timeout
	}

	info, err := os.Stat(*efivarsDir)
	uefi := err == nil && info.IsDir()

	lock, err := publish.AcquireLock(publish.DefaultLockPath(*bootStorage))
	if err != nil {
		return fail(nextBootFailed, "failed", err.Error())
	}
	defer lock.Close()
	dir, err := mountBootStorage(*bootStorage, !uefi)
	if err != nil {
		return fail(nextBootFailed, "failed", err.Error())
	}
	sDir := *stateDir
	if sDir == "" {
		sDir = filepath.Join(dir, "vintner", "state")
	}
	m, err := manifest.Load(sDir)
	if err != nil {
		return fail(nextBootFailed, "failed", fmt.Sprintf("failed to load manifest: %v", err))
	}
	inv, err := nextboot.Scan(dir)
	if err != nil {
		return fail(nextBootFailed, "failed", err.Error())
	}

	// Resolve the generation that the request names by role.
	switch target {
	case "previous-good":
		t.Generation, err = nextboot.PreviousGood(m)
	case "variant":
		if *genFlag >= 0 {
			// Named by the caller, for example the generation that is running: a
			// variant has no counter (D-C), so it needs no tries left. Check
			// still wants the generation in the manifest and the variant's file.
			t.Generation = *genFlag
		} else {
			t.Generation, err = nextboot.BootsNext(uefi, inv)
		}
	}
	if err == nil {
		err = nextboot.Check(m, uefi, inv, t)
	}
	if err != nil {
		var ne *nextboot.Error
		if errors.As(err, &ne) {
			return fail(nextBootUnavailable, ne.Code, ne.Msg)
		}
		return fail(nextBootFailed, "failed", err.Error())
	}

	if uefi {
		err = nextboot.SetUEFI(*efivarsDir, t)
	} else {
		err = nextboot.SetBIOS(filepath.Join(dir, "grub", "grubenv"), t)
	}
	if err != nil {
		return fail(nextBootFailed, "failed", err.Error())
	}

	res := nextBootResult{OK: true, Target: string(t.Kind), Firmware: "bios"}
	if uefi {
		res.Firmware = "uefi"
	}
	switch t.Kind {
	case nextboot.Generation, nextboot.Variant:
		res.Generation = &t.Generation
		res.Variant = t.Variant
	case nextboot.Menu:
		res.Timeout = &t.Timeout
	}
	if *asJSON {
		_ = json.NewEncoder(stdout).Encode(res)
		return nextBootOK
	}
	switch t.Kind {
	case nextboot.Clear:
		fmt.Fprintln(stdout, "next boot: back to the normal order")
	case nextboot.Recovery:
		fmt.Fprintln(stdout, "next boot: recovery")
	case nextboot.Generation:
		fmt.Fprintf(stdout, "next boot: generation %d\n", t.Generation)
	case nextboot.Variant:
		fmt.Fprintf(stdout, "next boot: variant %s of generation %d\n", t.Variant, t.Generation)
	case nextboot.Menu:
		fmt.Fprintf(stdout, "next boot: the menu, %d seconds\n", t.Timeout)
	}
	return nextBootOK
}
