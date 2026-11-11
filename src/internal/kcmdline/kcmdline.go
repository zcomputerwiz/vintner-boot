// SPDX-License-Identifier: GPL-2.0-or-later

// Package kcmdline reads the product's kernel command-line options from
// /etc/kernel/cmdline and composes an image's command line from them (WO-0010
// 4.2, decision D-B): vintner adds the root, the read-only mount and the boot
// storage identity, and the file holds everything else.
package kcmdline

import (
	"errors"
	"fmt"
	"os"
	"strings"
)

// DefaultFile is where the options live, relative to the root file system.
const DefaultFile = "/etc/kernel/cmdline"

// MaxLen is the longest command line vintner composes. x86's kernel keeps 2048
// bytes including the terminating NUL; this leaves a margin for a variant's
// tokens (4.3), which are appended to the line.
const MaxLen = 1900

// Parse reads the options from the text of the file: one or more lines, blank
// lines and lines starting with "#" ignored, options separated by white space.
// It refuses what belongs to vintner (root=, ro, rw and anything under
// "vintner.") and anything that is not printable ASCII, which a GRUB file or a
// UKI section would carry badly.
func Parse(text string) ([]string, error) {
	var opts []string
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		for _, f := range strings.Fields(line) {
			if err := checkOption(f); err != nil {
				return nil, err
			}
			opts = append(opts, f)
		}
	}
	return opts, nil
}

func checkOption(f string) error {
	switch {
	case strings.HasPrefix(f, "root="):
		return fmt.Errorf("the option %q belongs to vintner and must not be in the product's command line (the root is the installation's)", f)
	case strings.HasPrefix(f, "vintner."):
		return fmt.Errorf("the option %q belongs to vintner and must not be in the product's command line", f)
	case f == "ro" || f == "rw":
		return fmt.Errorf("the option %q is set by vintner (the root is mounted read-only first)", f)
	}
	for i := 0; i < len(f); i++ {
		if f[i] < 0x21 || f[i] > 0x7e {
			return fmt.Errorf("the option %q holds a character that is not printable ASCII", f)
		}
	}
	return nil
}

// Load reads and parses the file. A missing file is not an error: found is
// false, and the publisher then carries the newest image's command line
// forward, as before the file existed.
func Load(path string) (opts []string, found bool, err error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	opts, err = Parse(string(data))
	if err != nil {
		return nil, true, fmt.Errorf("%s: %w", path, err)
	}
	return opts, true, nil
}

// Compose builds an image's command line: the root token, "ro", the product's
// options, and the boot storage token.
func Compose(root, bootStorage string, opts []string) (string, error) {
	out := append([]string{root, "ro"}, opts...)
	out = append(out, bootStorage)
	line := strings.Join(out, " ")
	if len(line) > MaxLen {
		return "", fmt.Errorf("the kernel command line would be %d bytes; at most %d are allowed", len(line), MaxLen)
	}
	return line, nil
}

// Rebuild composes the command line of a new image from the line of an earlier
// one: the earlier line's root and boot storage tokens are kept, and the
// product's options replace everything else. A line that holds no root or boot
// storage token comes back unchanged, for the publisher's own validation to
// refuse.
func Rebuild(earlier string, opts []string) (string, error) {
	var root, bs string
	for _, f := range strings.Fields(earlier) {
		switch {
		case root == "" && strings.HasPrefix(f, "root="):
			root = f
		case bs == "" && strings.HasPrefix(f, "vintner.bootstorage="):
			bs = f
		}
	}
	if root == "" || bs == "" {
		return earlier, nil
	}
	return Compose(root, bs, opts)
}
