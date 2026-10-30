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
		osExit(2)
	}
	switch os.Args[1] {
	case "bless":
		cmdBless(os.Args[2:])
	case "manifest":
		cmdManifest(os.Args[2:])
	case "publish":
		cmdPublish(os.Args[2:])
	case "reconcile":
		cmdReconcile(os.Args[2:])
	case "secureboot":
		cmdSecureBoot(os.Args[2:])
	case "help", "-h", "--help":
		usage()
	default:
		fmt.Fprintf(os.Stderr, "vintner: unknown command %q\n", os.Args[1])
		usage()
		osExit(2)
	}
	osExit(0) // unmounts boot storage if a command mounted it
}

func usage() {
	fmt.Fprintln(os.Stderr, "usage: vintner <command> [arguments]\n\nAvailable commands:\n  bless      Bless the current boot\n  manifest   Manage the installation manifest\n  publish    Publish kernel and initramfs boot image\n  reconcile  Reconcile boot state\n  secureboot Initialize machine Secure Boot keys")
}
