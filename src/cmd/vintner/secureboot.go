// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

func cmdSecureBoot(args []string) {
	if len(args) < 1 {
		usageSecureBoot()
		osExit(2)
		return
	}

	switch args[0] {
	case "init":
		cmdSecureBootInit(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "vintner secureboot: unknown subcommand %q\n", args[0])
		usageSecureBoot()
		osExit(2)
	}
}

func usageSecureBoot() {
	fmt.Fprintln(os.Stderr, "usage: vintner secureboot <subcommand> [arguments]\n\nAvailable subcommands:\n  init  Initialize machine Secure Boot keys")
}

func cmdSecureBootInit(args []string) {
	fs := flag.NewFlagSet("vintner secureboot init", flag.ExitOnError)
	dir := fs.String("dir", "/etc/vintner/secureboot", "directory to store keys and certificates")
	installationID := fs.String("installation-id", "", "installation UUID")
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount partition named by vintner.bootstorage=)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")

	fs.Parse(args)

	instID := *installationID
	if instID == "" {
		sDir := *stateDir
		if sDir == "" {
			bsDir, err := mountBootStorage(*bootStorage, false)
			if err != nil {
				fmt.Fprintf(os.Stderr, "vintner secureboot init: no installation ID provided and could not access boot storage: %v\n", err)
				osExit(1)
				return
			}
			sDir = filepath.Join(bsDir, "vintner", "state")
		}
		m, err := manifest.Load(sDir)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vintner secureboot init: no installation ID provided and failed to load manifest: %v\n", err)
			osExit(1)
			return
		}
		if m.InstallationID == "" {
			fmt.Fprintln(os.Stderr, "vintner secureboot init: manifest has empty installation ID")
			osExit(1)
			return
		}
		instID = m.InstallationID
	}

	keyPath := filepath.Join(*dir, "key.pem")
	certPEMPath := filepath.Join(*dir, "cert.pem")
	certDERPath := filepath.Join(*dir, "cert.der")

	for _, p := range []string{keyPath, certPEMPath, certDERPath} {
		if _, err := os.Stat(p); err == nil {
			fmt.Fprintf(os.Stderr, "vintner secureboot init: file already exists: %s\n", p)
			osExit(1)
			return
		}
	}

	fp, err := secureboot.GenerateKey(*dir, instID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner secureboot init: %v\n", err)
		osExit(1)
		return
	}

	fmt.Printf("certificate: %s\n", certPEMPath)
	fmt.Printf("der: %s\n", certDERPath)
	fmt.Printf("sha256: %s\n", fp)
	fmt.Printf("subject: CN=vintner machine %s\n", instID)
}
