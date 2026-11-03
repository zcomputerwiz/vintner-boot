// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func captureOutput(f func()) (string, string) {
	origStdout := os.Stdout
	origStderr := os.Stderr

	rOut, wOut, _ := os.Pipe()
	rErr, wErr, _ := os.Pipe()

	os.Stdout = wOut
	os.Stderr = wErr

	outC := make(chan string)
	errC := make(chan string)

	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, rOut)
		outC <- buf.String()
	}()

	go func() {
		var buf bytes.Buffer
		io.Copy(&buf, rErr)
		errC <- buf.String()
	}()

	f()

	wOut.Close()
	wErr.Close()

	stdoutStr := <-outC
	stderrStr := <-errC

	os.Stdout = origStdout
	os.Stderr = origStderr

	return stdoutStr, stderrStr
}

func TestSecureBootInitFlag(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	osExit = func(code int) {
		t.Fatalf("unexpected osExit(%d)", code)
	}

	dir := t.TempDir()
	instID := "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f"

	stdout, stderr := captureOutput(func() {
		cmdSecureBoot([]string{"init", "--dir", dir, "--installation-id", instID})
	})

	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 output lines, got %d: %q", len(lines), stdout)
	}

	certPath := filepath.Join(dir, "cert.pem")
	derPath := filepath.Join(dir, "cert.der")

	if lines[0] != "certificate: "+certPath {
		t.Errorf("line 0: got %q, want %q", lines[0], "certificate: "+certPath)
	}
	if lines[1] != "der: "+derPath {
		t.Errorf("line 1: got %q, want %q", lines[1], "der: "+derPath)
	}
	if !strings.HasPrefix(lines[2], "sha256: ") || len(strings.TrimPrefix(lines[2], "sha256: ")) != 64 {
		t.Errorf("line 2 invalid sha256: %q", lines[2])
	}
	expectedSubject := "subject: CN=vintner machine " + instID
	if lines[3] != expectedSubject {
		t.Errorf("line 3: got %q, want %q", lines[3], expectedSubject)
	}

	// Verify key was never printed
	if strings.Contains(stdout, "BEGIN PRIVATE KEY") || strings.Contains(stdout, "key.pem") {
		t.Errorf("private key material or path unexpectedly printed in stdout: %s", stdout)
	}

	// Refusal to overwrite: running again must exit non-zero
	var exitCode int
	osExit = func(code int) {
		exitCode = code
		panic("exit")
	}

	defer func() {
		r := recover()
		if r == nil {
			t.Fatal("expected second run to call osExit")
		}
		if exitCode == 0 {
			t.Errorf("expected non-zero exit code on overwrite, got %d", exitCode)
		}
	}()

	cmdSecureBoot([]string{"init", "--dir", dir, "--installation-id", instID})
}

func TestSecureBootInitFromManifest(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()
	osExit = func(code int) {
		t.Fatalf("unexpected osExit(%d)", code)
	}

	stateDir := t.TempDir()
	instID := "11111111-2222-3333-4444-555555555555"

	m := &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: instID,
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       "uefi",
		SecureBootTier: "none",
		Root: manifest.Root{
			PartUUID: "6a1f3c2e-0d5b-4f7e-9a8c-1b2d3e4f5a6b",
			UUID:     "root-uuid",
		},
		BootStorage: manifest.BootStorage{
			PartUUID: "9d8c7b6a-5f4e-4d3c-2b1a-0e9f8d7c6b5a",
		},
		Tries:          3,
		Retain:         3,
		NextGeneration: 2,
		Images:         []manifest.Image{},
		Recovery:       []manifest.Recovery{},
	}
	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatalf("save manifest: %v", err)
	}

	keyDir := t.TempDir()
	stdout, stderr := captureOutput(func() {
		cmdSecureBoot([]string{"init", "--dir", keyDir, "--state-dir", stateDir})
	})

	if stderr != "" {
		t.Errorf("unexpected stderr: %s", stderr)
	}

	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 4 {
		t.Fatalf("expected 4 output lines, got %d: %q", len(lines), stdout)
	}
	expectedSubject := "subject: CN=vintner machine " + instID
	if lines[3] != expectedSubject {
		t.Errorf("line 3: got %q, want %q", lines[3], expectedSubject)
	}
}
