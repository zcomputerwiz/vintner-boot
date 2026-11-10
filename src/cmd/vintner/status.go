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

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
	"github.com/zcomputerwiz/vintner-boot/src/internal/status"
)

// Exit codes of vintner status (docs/status.md).
const (
	statusOK     = 0 // the report was made; health findings do not change it
	statusFailed = 1 // an I/O or state error
	statusUsage  = 2
	statusNone   = 3 // not a vintner installation: no valid manifest
)

func cmdStatus(args []string) {
	osExit(runStatus(args, os.Stdout, os.Stderr))
}

// statusError is the --json output when there is no report.
type statusError struct {
	OK    bool   `json:"ok"`
	Code  string `json:"code"`
	Error string `json:"error"`
}

// runStatus implements vintner status (WO-0010 4.5). It reads and writes
// nothing, and takes no lock: a product calls it early in boot and must not wait
// behind a publish.
func runStatus(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner status", flag.ContinueOnError)
	fs.SetOutput(stderr)
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount the partition named by vintner.bootstorage= read-only on demand)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")
	efivarsDir := fs.String("efivars", efivar.DefaultDir, "efivars directory")
	secureBootDir := fs.String("secureboot-dir", "/etc/vintner/secureboot", "directory holding machine keys and certificates")
	cmdlineFile := fs.String("cmdline", "/proc/cmdline", "the running kernel's command line")
	sigEnforce := fs.String("sig-enforce", status.DefaultSigEnforce, "the kernel's module signature enforcement parameter")
	asJSON := fs.Bool("json", false, "print the status as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: vintner status [--json]")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return statusUsage
	}
	if fs.NArg() != 0 {
		fmt.Fprintf(stderr, "vintner status: unexpected argument %q\n", fs.Arg(0))
		return statusUsage
	}

	fail := func(code int, kind string, err error) int {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(statusError{Code: kind, Error: err.Error()})
		} else {
			fmt.Fprintf(stderr, "vintner status: %v\n", err)
		}
		return code
	}

	info, err := os.Stat(*efivarsDir)
	uefi := err == nil && info.IsDir()

	dir, err := mountBootStorage(*bootStorage, false)
	if err != nil {
		return fail(statusFailed, "failed", err)
	}
	sDir := *stateDir
	if sDir == "" {
		sDir = filepath.Join(dir, "vintner", "state")
	}
	m, err := manifest.Load(sDir)
	if err != nil {
		if errors.Is(err, manifest.ErrNoValidCopy) {
			return fail(statusNone, "not-vintner", fmt.Errorf("no valid manifest in %s: this is not a vintner installation", sDir))
		}
		return fail(statusFailed, "failed", fmt.Errorf("failed to load manifest: %w", err))
	}
	v, err := state.Scan(dir)
	if err != nil {
		return fail(statusFailed, "failed", fmt.Errorf("failed to scan boot storage: %w", err))
	}
	_, reports := state.Reconcile(m, v)
	cmdline, _ := os.ReadFile(*cmdlineFile)

	r := status.Build(status.Inputs{
		Manifest:       m,
		View:           v,
		Cmdline:        string(cmdline),
		IsUEFI:         uefi,
		EfivarsDir:     *efivarsDir,
		SecureBootDir:  *secureBootDir,
		SigEnforcePath: *sigEnforce,
		Reports:        reports,
	})
	if *asJSON {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(r)
		return statusOK
	}
	printStatus(stdout, r)
	return statusOK
}

func printStatus(w io.Writer, r *status.Report) {
	fmt.Fprintf(w, "firmware: %s (booted through %s), secure boot tier %s\n", r.Firmware, r.BootedFirmware, r.SecureBootTier)
	for _, g := range r.Generations {
		tries := ""
		if g.TriesLeft != nil {
			tries = fmt.Sprintf(", %d tries left", *g.TriesLeft)
		}
		fmt.Fprintf(w, "generation %d: %s, %s, kernel %s%s\n", g.Generation, g.State, g.Date, g.ABI, tries)
		for _, v := range g.Variants {
			fmt.Fprintf(w, "    variant %s: %s\n", v.ID, v.Title)
		}
	}
	b := r.Booted
	switch b.Kind {
	case status.KindGeneration:
		fmt.Fprintf(w, "booted: generation %d", *b.Generation)
	case status.KindVariant:
		fmt.Fprintf(w, "booted: variant %s of generation %d", b.Variant, *b.Generation)
	case status.KindRecovery:
		fmt.Fprintf(w, "booted: recovery %s", b.Slot)
	default:
		fmt.Fprint(w, "booted: not a vintner entry")
	}
	if b.FailedAttempts != nil {
		fmt.Fprintf(w, ", %d earlier boots failed", *b.FailedAttempts)
	}
	fmt.Fprintln(w)
	fmt.Fprintf(w, "out-of-tree modules: %s\n", r.Modules.OutOfTree)
	if r.Healthy {
		fmt.Fprintln(w, "health: ok")
		return
	}
	for _, h := range r.Health {
		fmt.Fprintf(w, "health: %s\n", h)
	}
}
