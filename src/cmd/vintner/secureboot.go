// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
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
	case "request":
		osExit(runSecureBootRequest(args[1:], os.Stdout, os.Stderr))
	default:
		fmt.Fprintf(os.Stderr, "vintner secureboot: unknown subcommand %q\n", args[0])
		usageSecureBoot()
		osExit(2)
	}
}

func usageSecureBoot() {
	fmt.Fprintln(os.Stderr, "usage: vintner secureboot <subcommand> [arguments]\n\nAvailable subcommands:\n  init     Initialize machine Secure Boot keys\n  request  Ask MokManager to enroll the machine certificate at the next boot (tier B)")
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

// runSecureBootRequest implements vintner secureboot request: tier B's request
// alone (docs/secure-boot.md §8.4). It makes the machine key when there is none,
// and asks MokManager to enroll its certificate at the next boot unless shim's
// MOK list already holds it. vintner install --secure-boot B keeps the key.
func runSecureBootRequest(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("vintner secureboot request", flag.ContinueOnError)
	fs.SetOutput(stderr)
	dir := fs.String("dir", "/etc/vintner/secureboot", "the machine key set (made there when it does not exist)")
	efivars := fs.String("efivars", efivar.DefaultDir, "the efivarfs directory")
	passwordFile := fs.String("password-file", "", "a file holding the one-time password for MokManager (default: one is made and printed)")
	installationID := fs.String("installation-id", "", "the installation UUID for the certificate's subject, when the key is made (default: a new random one)")
	if err := fs.Parse(args); err != nil {
		return 2
	}
	fail := func(format string, a ...any) int {
		fmt.Fprintf(stderr, "vintner secureboot request: "+format+"\n", a...)
		return 1
	}
	if st, err := os.Stat(*efivars); err != nil || !st.IsDir() {
		return fail("no UEFI variables at %s: the request is written into the firmware's variables, so run this on the machine, booted through UEFI", *efivars)
	}
	password := ""
	if *passwordFile != "" {
		b, err := os.ReadFile(*passwordFile)
		if err != nil {
			return fail("%v", err)
		}
		password = strings.TrimRight(string(b), "\r\n")
		if err := secureboot.ValidatePassword(password); err != nil {
			return fail("%v", err)
		}
	}

	paths := []string{filepath.Join(*dir, "key.pem"), filepath.Join(*dir, "cert.pem"), filepath.Join(*dir, "cert.der")}
	n := 0
	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			n++
		}
	}
	switch n {
	case 0:
		id := *installationID
		if id == "" {
			b := make([]byte, 16)
			if _, err := rand.Read(b); err != nil {
				return fail("%v", err)
			}
			b[6], b[8] = b[6]&0x0f|0x40, b[8]&0x3f|0x80
			h := hex.EncodeToString(b)
			id = h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
		}
		fp, err := secureboot.GenerateKey(*dir, id)
		if err != nil {
			return fail("%v", err)
		}
		fmt.Fprintf(stdout, "created the machine key in %s (certificate sha256 %s)\n", *dir, fp)
	case 3:
		if err := secureboot.KeyMatchesCert(paths[0], paths[1]); err != nil {
			return fail("%v", err)
		}
	default:
		return fail("%s holds part of a key set (%d of key.pem, cert.pem and cert.der): move it away", *dir, n)
	}
	der, err := os.ReadFile(paths[2])
	if err != nil {
		return fail("%v", err)
	}
	mokVarDir := ""
	if *efivars == efivar.DefaultDir {
		mokVarDir = secureboot.DefaultMOKVarDir
	}
	if ok, err := secureboot.MOKEnrolled(*efivars, mokVarDir, der); err != nil {
		return fail("%v", err)
	} else if ok {
		fmt.Fprintf(stdout, "the machine certificate %s is in shim's MOK list already; no request was written\n", paths[2])
		return 0
	}
	if password == "" {
		if password, err = secureboot.GeneratePassword(nil); err != nil {
			return fail("%v", err)
		}
	}
	if err := secureboot.WriteMOKRequest(*efivars, der, password); err != nil {
		return fail("%v", err)
	}
	printMOKRequest(stdout, password, *passwordFile != "")
	return 0
}

// printMOKRequest tells the person what to do at the console. The steps are
// the menu items MokManager shows (ci/mok-chain E1).
func printMOKRequest(w io.Writer, password string, fromFile bool) {
	pw := password
	if fromFile {
		pw = "the one in the password file"
	}
	fmt.Fprintf(w, `Secure Boot tier B: a request for MokManager is written. At the next boot shim starts MokManager, which shows its menu and waits for a person at the console:
  1. choose "Enroll MOK", then "Continue" and "Yes"
  2. type the one-time password: %s
  3. choose "Reboot"
Until then nothing vintner signed starts with Secure Boot on. If the request is lost, choose "Enroll key from disk" in MokManager and the file EFI/vintner/machine-cert.cer on the ESP.
`, pw)
}
