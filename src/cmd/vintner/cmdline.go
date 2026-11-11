// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/zcomputerwiz/vintner-boot/src/internal/kcmdline"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

// Exit codes of vintner cmdline (docs/cmdline.md).
const (
	cmdlineOK      = 0
	cmdlineFailed  = 1 // an I/O error, or the publish failed
	cmdlineUsage   = 2
	cmdlineRefused = 3 // an option that belongs to vintner, or a command line that is too long
)

// publishFn is publish.Publish; tests replace it.
var publishFn = publish.Publish

func cmdCmdline(args []string) {
	osExit(runCmdline(args, os.Stdout, os.Stderr))
}

// cmdlineResult is the --json output, on success and on failure.
type cmdlineResult struct {
	OK         bool     `json:"ok"`
	File       string   `json:"file,omitempty"`
	Exists     *bool    `json:"exists,omitempty"`
	Options    []string `json:"options"`
	Running    string   `json:"running,omitempty"`
	Published  *bool    `json:"published,omitempty"`
	Unchanged  *bool    `json:"unchanged,omitempty"`
	Generation *int     `json:"generation,omitempty"`
	Code       string   `json:"code,omitempty"`
	Error      string   `json:"error,omitempty"`
}

// runCmdline implements vintner cmdline show|set (WO-0010 4.2, D-B). "set"
// writes /etc/kernel/cmdline and publishes a new generation for the running
// kernel, so a bad option is an ordinary TRYING image that falls back to the
// previous GOOD one with the old line.
func runCmdline(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner cmdline", flag.ContinueOnError)
	fs.SetOutput(stderr)
	file := fs.String("file", "", "the options file (default <root-dir>/etc/kernel/cmdline)")
	rootDir := fs.String("root-dir", "/", "root filesystem directory")
	kernel := fs.String("kernel", "", "set: kernel ABI to publish (default: the running kernel)")
	bootStorage := fs.String("boot-storage", "", "set: boot storage directory (default: mount on demand)")
	stateDir := fs.String("state-dir", "", "set: manifest directory (default <boot-storage>/vintner/state)")
	procCmdline := fs.String("proc-cmdline", "/proc/cmdline", "show: the running kernel's command line")
	noPublish := fs.Bool("no-publish", false, "set: write the file and publish nothing (the next publish uses it)")
	clear := fs.Bool("clear", false, "set: remove every option")
	asJSON := fs.Bool("json", false, "print the result as one JSON object")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: vintner cmdline [--json] show\n       vintner cmdline [--json] [--no-publish] set (--clear | <option>...)")
		fs.PrintDefaults()
	}
	// Flags may come before or after the verb.
	if err := fs.Parse(args); err != nil {
		return cmdlineUsage
	}
	rest := fs.Args()
	if len(rest) == 0 {
		fs.Usage()
		return cmdlineUsage
	}
	verb, operands := rest[0], rest[1:]
	if err := fs.Parse(operands); err != nil {
		return cmdlineUsage
	}
	operands = fs.Args()

	path := *file
	if path == "" {
		path = filepath.Join(*rootDir, kcmdline.DefaultFile)
	}
	fail := func(code int, kind string, err error) int {
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(cmdlineResult{Options: []string{}, Code: kind, Error: err.Error()})
		} else {
			fmt.Fprintf(stderr, "vintner cmdline: %v\n", err)
		}
		return code
	}
	emit := func(r cmdlineResult) {
		r.OK = true
		if r.Options == nil {
			r.Options = []string{}
		}
		if *asJSON {
			_ = json.NewEncoder(stdout).Encode(r)
			return
		}
		if r.Exists != nil && !*r.Exists {
			fmt.Fprintf(stdout, "%s does not exist: each new image carries the newest image's command line forward\n", r.File)
		} else {
			fmt.Fprintln(stdout, strings.Join(r.Options, " "))
		}
	}

	switch verb {
	case "show":
		if len(operands) != 0 {
			fmt.Fprintf(stderr, "vintner cmdline show: unexpected argument %q\n", operands[0])
			return cmdlineUsage
		}
		opts, found, err := kcmdline.Load(path)
		if err != nil {
			return fail(cmdlineRefused, "refused", err)
		}
		running, _ := os.ReadFile(*procCmdline)
		emit(cmdlineResult{File: path, Exists: &found, Options: opts, Running: strings.TrimSpace(string(running))})
		return cmdlineOK

	case "set":
		if *clear == (len(operands) > 0) {
			fmt.Fprintln(stderr, "vintner cmdline set: give the options, or --clear to remove them all")
			return cmdlineUsage
		}
		opts, err := kcmdline.Parse(strings.Join(operands, " "))
		if err != nil {
			return fail(cmdlineRefused, "refused", err)
		}
		// The whole line must fit, whatever the root and the boot storage are:
		// UUIDs have a fixed length.
		if _, err := kcmdline.Compose("root=PARTUUID=00000000-0000-0000-0000-000000000000", "vintner.bootstorage=PARTUUID=00000000-0000-0000-0000-000000000000", opts); err != nil {
			return fail(cmdlineRefused, "refused", err)
		}

		previous, readErr := os.ReadFile(path)
		existed := readErr == nil
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return fail(cmdlineFailed, "failed", readErr)
		}
		text := ""
		if len(opts) > 0 {
			text = strings.Join(opts, " ") + "\n"
		}
		if err := writeFileAtomic(path, []byte(text)); err != nil {
			return fail(cmdlineFailed, "failed", err)
		}
		res := cmdlineResult{File: path, Options: opts}
		pub := false
		res.Published = &pub
		if *noPublish {
			emit(res)
			return cmdlineOK
		}

		abi := *kernel
		if abi == "" {
			if abi, err = runningKernel(); err != nil {
				restoreFile(path, previous, existed)
				return fail(cmdlineFailed, "failed", err)
			}
		}
		out := stdout
		if *asJSON {
			out = stderr // publish prints progress lines; stdout holds the one JSON object
		}
		r, err := publishFn(context.Background(), publish.Options{
			KernelABI:         abi,
			BootStorageDir:    *bootStorage,
			StateDir:          *stateDir,
			RootDir:           *rootDir,
			KernelCmdlineFile: path,
			Stdout:            out,
			Stderr:            stderr,
		})
		if err != nil {
			// The file must not differ from what is published: the next kernel
			// update would otherwise publish an option nobody saw start.
			restoreFile(path, previous, existed)
			return fail(cmdlineFailed, "failed", fmt.Errorf("publishing the new generation failed, the options file is back as it was: %w", err))
		}
		pub = true
		res.Unchanged = &r.Unchanged
		if !r.Unchanged {
			res.Generation = &r.Generation
		}
		emit(res)
		return cmdlineOK
	}
	fmt.Fprintf(stderr, "vintner cmdline: unknown verb %q\n", verb)
	fs.Usage()
	return cmdlineUsage
}

// writeFileAtomic replaces path with data: a temporary file in the same
// directory, synced, then renamed over it.
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".cmdline-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o644); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// restoreFile puts the file back as it was, or removes it if it did not exist.
func restoreFile(path string, previous []byte, existed bool) {
	if existed {
		_ = writeFileAtomic(path, previous)
		return
	}
	_ = os.Remove(path)
}

// runningKernel is the ABI of the running kernel, as uname -r prints it.
func runningKernel() (string, error) {
	var u syscall.Utsname
	if err := syscall.Uname(&u); err != nil {
		return "", fmt.Errorf("cannot tell the running kernel: %w", err)
	}
	var b []byte
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b = append(b, byte(c))
	}
	return string(b), nil
}
