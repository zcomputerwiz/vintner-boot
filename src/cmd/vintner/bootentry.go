// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
	"github.com/zcomputerwiz/vintner-boot/src/internal/install"
)

func cmdBootEntry(args []string) {
	osExit(runBootEntry(args, os.Stdout, os.Stderr))
}

// runBootEntry implements vintner boot-entry: the explicit command that
// (re)creates vintner's NVRAM boot entry and puts it first in BootOrder. The
// health report names it; nothing else recreates the entry (threat model T8).
func runBootEntry(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner boot-entry", flag.ContinueOnError)
	fs.SetOutput(stderr)
	disk := fs.String("disk", "", "the whole disk that holds the ESP (required)")
	espUUID := fs.String("esp-partuuid", "", "PARTUUID of the EFI System Partition (default: the one partition of the ESP type)")
	espDir := fs.String("esp", "", "the mounted ESP, to check that vintner's loader is on it")
	efivars := fs.String("efivars", efivar.DefaultDir, "the efivarfs directory")
	check := fs.Bool("check", false, "only report the state of the entry and BootOrder; change nothing")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if *disk == "" {
		fmt.Fprintln(stderr, "vintner boot-entry: --disk is required")
		return 2
	}
	if *check {
		r, err := efivar.BootOrderReport(*efivars)
		if err != nil {
			fmt.Fprintf(stderr, "vintner boot-entry: %v\n", err)
			return 1
		}
		if r != "" {
			fmt.Fprintln(stdout, r)
			return 1
		}
		fmt.Fprintln(stdout, "the vintner boot entry exists and BootOrder starts with it")
		return 0
	}
	f, err := os.Open(*disk)
	if err != nil {
		fmt.Fprintf(stderr, "vintner boot-entry: %v\n", err)
		return 1
	}
	defer f.Close()
	size, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		fmt.Fprintf(stderr, "vintner boot-entry: %v\n", err)
		return 1
	}
	t, err := gpt.ReadAt(f, size)
	if err != nil {
		fmt.Fprintf(stderr, "vintner boot-entry: %v\n", err)
		return 1
	}
	var esp gpt.Partition
	if *espUUID != "" {
		p, ok := t.ByUUID(*espUUID)
		if !ok {
			fmt.Fprintf(stderr, "vintner boot-entry: no partition with PARTUUID %s\n", *espUUID)
			return 1
		}
		esp = p
	} else if ps := t.ByType(gpt.TypeESP); len(ps) == 1 {
		esp = ps[0]
	} else {
		fmt.Fprintf(stderr, "vintner boot-entry: %d EFI System Partitions on %s; name one with --esp-partuuid\n", len(ps), *disk)
		return 1
	}
	num, created, err := install.SetBootEntry(*efivars, *espDir, esp)
	if err != nil {
		fmt.Fprintf(stderr, "vintner boot-entry: %v\n", err)
		return 1
	}
	if created {
		fmt.Fprintf(stdout, "created Boot%04X %q and put it first in BootOrder\n", num, efivar.VintnerDescription)
	} else {
		fmt.Fprintf(stdout, "Boot%04X %q is first in BootOrder\n", num, efivar.VintnerDescription)
	}
	return 0
}
