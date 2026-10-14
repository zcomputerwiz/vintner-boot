// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func cmdManifest(args []string) {
	if len(args) < 2 {
		usageManifest()
		os.Exit(2)
	}

	switch args[0] {
	case "show":
		var stateDir string
		for i := 1; i < len(args); i++ {
			if args[i] == "--state-dir" {
				if i+1 < len(args) {
					stateDir = args[i+1]
					i++
				} else {
					fmt.Fprintln(os.Stderr, "vintner manifest show: missing argument for --state-dir")
					usageManifest()
					os.Exit(2)
				}
			} else {
				fmt.Fprintf(os.Stderr, "vintner manifest show: unknown argument %q\n", args[i])
				usageManifest()
				os.Exit(2)
			}
		}

		if stateDir == "" {
			fmt.Fprintln(os.Stderr, "vintner manifest show: --state-dir is required")
			usageManifest()
			os.Exit(2)
		}

		m, err := manifest.Load(stateDir)
		if err != nil {
			if err == manifest.ErrNoValidCopy {
				fmt.Fprintln(os.Stderr, "vintner manifest show: no valid copy")
			} else {
				fmt.Fprintf(os.Stderr, "vintner manifest show: failed to load: %v\n", err)
			}
			os.Exit(1)
		}

		fmt.Printf("Loaded from copy %s, seq %d\n", m.LoadedCopy, m.Seq)

		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.SetEscapeHTML(false)
		if err := enc.Encode(m); err != nil {
			fmt.Fprintf(os.Stderr, "vintner manifest show: failed to encode output: %v\n", err)
			os.Exit(1)
		}

	case "help", "-h", "--help":
		usageManifest()
	default:
		fmt.Fprintf(os.Stderr, "vintner manifest: unknown command %q\n", args[0])
		usageManifest()
		os.Exit(2)
	}
}

func usageManifest() {
	fmt.Fprintln(os.Stderr, "usage: vintner manifest show --state-dir DIR")
}
