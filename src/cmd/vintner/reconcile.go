// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
)

func cmdReconcile(args []string) {
	fs := flag.NewFlagSet("vintner reconcile", flag.ExitOnError)
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount the partition named by vintner.bootstorage= on demand)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")
	dryRun := fs.Bool("dry-run", false, "print actions but do not perform them")

	fs.Parse(args)

	if !*dryRun {
		lockPath := publish.DefaultLockPath(*bootStorage)
		lock, err := publish.AcquireLock(lockPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vintner reconcile: %v\n", err)
			osExit(1)
			return
		}
		defer lock.Close()
	}

	dir, err := mountBootStorage(*bootStorage, !*dryRun)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner reconcile: %v\n", err)
		osExit(1)
		return
	}
	*bootStorage = dir

	sDir := *stateDir
	if sDir == "" {
		sDir = filepath.Join(*bootStorage, "vintner", "state")
	}

	m, err := manifest.Load(sDir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner reconcile: failed to load manifest: %v\n", err)
		osExit(1)
		return
	}

	oldJSON, _ := json.Marshal(m)

	v, err := state.Scan(*bootStorage)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner reconcile: failed to scan boot storage: %v\n", err)
		osExit(1)
		return
	}

	actions, reports := state.Reconcile(m, v)

	for _, r := range reports {
		fmt.Println("REPORT:", r)
	}

	for _, a := range actions {
		fmt.Printf("ACTION: %s %s\n", a.Type, a.Name)
		if !*dryRun {
			if a.Type == "remove_staging" {
				p := filepath.Join(*bootStorage, "vintner", "staging", a.Name)
				if err := os.RemoveAll(p); err != nil { // a BIOS staging entry is a directory
					fmt.Fprintf(os.Stderr, "vintner reconcile: failed to remove %s: %v\n", p, err)
					osExit(1)
					return
				}
			}
		}
	}

	if !*dryRun {
		newJSON, _ := json.Marshal(m)
		if string(oldJSON) != string(newJSON) {
			if err := m.Validate(); err != nil {
				fmt.Fprintf(os.Stderr, "vintner reconcile: validation failed after reconcile: %v\n", err)
				osExit(1)
				return
			}
			if err := manifest.Save(sDir, m); err != nil {
				fmt.Fprintf(os.Stderr, "vintner reconcile: failed to save manifest: %v\n", err)
				osExit(1)
				return
			}
		}
	}

	if len(reports) > 0 {
		osExit(1)
	} else {
		osExit(0)
	}
}
