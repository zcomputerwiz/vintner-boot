// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
)

func cmdBless(args []string) {
	fs := flag.NewFlagSet("vintner bless", flag.ExitOnError)
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount the partition named by vintner.bootstorage= on demand)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")
	efivarsDir := fs.String("efivars", "/sys/firmware/efi/efivars", "efivars directory")

	fs.Parse(args)

	lockPath := publish.DefaultLockPath(*bootStorage)
	lock, err := publish.AcquireLock(lockPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: %v\n", err)
		osExit(1)
		return
	}
	defer lock.Close()

	dir, err := mountBootStorage(*bootStorage, true)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: %v\n", err)
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
		fmt.Fprintf(os.Stderr, "vintner bless: failed to load manifest: %v\n", err)
		osExit(1)
		return
	}

	var bootedGen int

	info, err := os.Stat(*efivarsDir)
	isUEFI := err == nil && info.IsDir()

	if isUEFI {
		_, val, err := efivar.GetString(*efivarsDir, "LoaderBootCountPath", efivar.LoaderInfo)
		if err != nil {
			if errors.Is(err, efivar.ErrNotFound) {
				fmt.Fprintln(os.Stderr, "vintner bless: LoaderBootCountPath not found, entry was not counted")
				osExit(0)
			}
			fmt.Fprintf(os.Stderr, "vintner bless: failed to read LoaderBootCountPath: %v\n", err)
			osExit(1)
		}

		// e.g. \EFI\Linux\vintner-os-42+2-1.efi
		filename := val[strings.LastIndexAny(val, `\/`)+1:]

		parsed, ok := bls.Parse(filename)
		if !ok {
			fmt.Fprintf(os.Stderr, "vintner bless: not an EFI file: %s\n", filename)
			osExit(1)
		}
		stem := parsed.Stem

		if _, ok := bls.IsVintnerRecovery(stem); ok {
			osExit(0)
		}

		gen, ok := bls.IsVintnerOrdinary(stem)
		if !ok {
			fmt.Fprintf(os.Stderr, "vintner bless: not a vintner ordinary entry: %s\n", stem)
			osExit(1)
		}

		efiDir := filepath.Join(*bootStorage, "EFI", "Linux")
		if err := bls.Rearm(efiDir, stem, m.Tries); err != nil {
			fmt.Fprintf(os.Stderr, "vintner bless: failed to rearm: %v\n", err)
			osExit(1)
		}

		bootedGen = gen
	} else {
		// BIOS
		envPath := filepath.Join(*bootStorage, "grub", "grubenv")
		env, err := grubenv.Read(envPath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vintner bless: failed to read grubenv: %v\n", err)
			osExit(1)
		}

		val, ok := env.Get("VINTNER_BOOTED")
		if !ok || val == "" || val == "RA" {
			osExit(0)
		}

		gen, err := strconv.Atoi(val)
		if err != nil {
			fmt.Fprintf(os.Stderr, "vintner bless: invalid VINTNER_BOOTED: %v\n", val)
			osExit(1)
		}

		env.Set(fmt.Sprintf("VINTNER_T_%d", gen), strings.Repeat("x", m.Tries))
		env.Unset("VINTNER_BOOTED")

		if err := grubenv.Write(envPath, env); err != nil {
			fmt.Fprintf(os.Stderr, "vintner bless: failed to write grubenv: %v\n", err)
			osExit(1)
		}

		bootedGen = gen
	}

	// Update manifest
	foundRecord := false
	for i := range m.Images {
		if m.Images[i].Generation == bootedGen {
			m.Images[i].State = "GOOD"
			now := time.Now().UTC().Format(time.RFC3339)
			if m.Images[i].FirstGood == nil {
				m.Images[i].FirstGood = &now
			}
			m.Images[i].LastGood = &now
			foundRecord = true
			break
		}
	}

	if !foundRecord {
		fmt.Fprintf(os.Stderr, "vintner bless: generation %d has no manifest record\n", bootedGen)
		osExit(1)
	}

	if err := m.Validate(); err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: manifest validation failed: %v\n", err)
		osExit(1)
	}

	if err := manifest.Save(sDir, m); err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: failed to save manifest: %v\n", err)
		osExit(1)
		return
	}

	// Reconcile boot storage after re-arming booted image
	origJSON, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: %v\n", err)
		osExit(1)
		return
	}

	origImages := append([]manifest.Image(nil), m.Images...)

	v, err := state.Scan(*bootStorage)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: failed to scan boot storage: %v\n", err)
		osExit(1)
		return
	}

	actions, reports := state.Reconcile(m, v)

	for _, r := range reports {
		fmt.Println(r)
	}

	for _, a := range actions {
		if a.Type == "remove_staging" {
			p := filepath.Join(*bootStorage, "vintner", "staging", a.Name)
			if err := os.RemoveAll(p); err != nil {
				fmt.Fprintf(os.Stderr, "vintner bless: failed to remove %s: %v\n", p, err)
				osExit(1)
				return
			}
		}
	}

	newStates := make(map[int]string, len(m.Images))
	for _, img := range m.Images {
		newStates[img.Generation] = img.State
	}
	for _, img := range origImages {
		if s, ok := newStates[img.Generation]; !ok {
			fmt.Printf("generation %d: %s -> removed\n", img.Generation, img.State)
		} else if s != img.State {
			fmt.Printf("generation %d: %s -> %s\n", img.Generation, img.State, s)
		}
	}

	newJSON, err := json.Marshal(m)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner bless: %v\n", err)
		osExit(1)
		return
	}

	if string(origJSON) != string(newJSON) {
		if err := m.Validate(); err != nil {
			fmt.Fprintf(os.Stderr, "vintner bless: validation failed after reconcile: %v\n", err)
			osExit(1)
			return
		}
		if err := manifest.Save(sDir, m); err != nil {
			fmt.Fprintf(os.Stderr, "vintner bless: failed to save manifest after reconcile: %v\n", err)
			osExit(1)
			return
		}
	}
}

var osExit = os.Exit
