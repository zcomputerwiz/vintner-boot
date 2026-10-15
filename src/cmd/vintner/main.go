// SPDX-License-Identifier: GPL-2.0-or-later

// Command vintner manages vintner-boot images, boot state and recovery.
package main

import (
	"fmt"
	"os"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "manifest":
		cmdManifest(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "vintner: unknown command %q\n", os.Args[1])
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: vintner <command> [arguments]\n\nAvailable commands:\n  manifest  Manage the installation manifest")
}
