package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

func TestCmdManifest(t *testing.T) {
	tmpDir := t.TempDir()

	validStateDir := filepath.Join(tmpDir, "valid")
	os.MkdirAll(validStateDir, 0755)
	validM := &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f",
		MachineID:      "3c9a0f4e6d2b4a8c9e1f2a3b4c5d6e7f",
		Firmware:       "uefi",
		SecureBootTier: "none",
		Root: manifest.Root{
			PartUUID: "6a1f3c2e-0d5b-4f7e-9a8c-1b2d3e4f5a6b",
			UUID:     "b1c2d3e4-f5a6-4b7c-8d9e-0f1a2b3c4d5e",
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
	if err := validM.Validate(); err != nil {
		t.Fatalf("failed to validate validM: %v", err)
	}
	manifest.Save(validStateDir, validM)

	emptyStateDir := filepath.Join(tmpDir, "empty")
	os.MkdirAll(emptyStateDir, 0755)

	invalidStateDir := filepath.Join(tmpDir, "invalid")
	os.MkdirAll(invalidStateDir, 0755)
	os.WriteFile(filepath.Join(invalidStateDir, "manifest.json"), []byte("invalid json"), 0644)

	cases := []struct {
		args     []string
		expected int
		stdout   string
		stderr   string
	}{
		{[]string{}, 2, "", "usage: vintner manifest show --state-dir DIR"},
		{[]string{"help", "arg"}, 0, "", "usage: vintner manifest show --state-dir DIR"}, // len >= 2
		{[]string{"-h", "arg"}, 0, "", "usage: vintner manifest show --state-dir DIR"},
		{[]string{"--help", "arg"}, 0, "", "usage: vintner manifest show --state-dir DIR"},
		{[]string{"unknown", "arg"}, 2, "", "vintner manifest: unknown command \"unknown\""},
		{[]string{"show"}, 2, "", "usage: vintner manifest show --state-dir DIR"},
		{[]string{"show", "arg"}, 2, "", "vintner manifest show: unknown argument \"arg\""},
		{[]string{"show", "--state-dir"}, 2, "", "vintner manifest show: missing argument for --state-dir"},
		{[]string{"show", "--state-dir", validStateDir, "--unknown"}, 2, "", "vintner manifest show: unknown argument \"--unknown\""},
		{[]string{"show", "--state-dir", emptyStateDir}, 1, "", "vintner manifest show: no valid copy"},
		{[]string{"show", "--state-dir", invalidStateDir}, 1, "", "vintner manifest show: no valid copy"},
		{[]string{"show", "--state-dir", validStateDir}, 0, "\"installation_id\": \"0f8e2d6c-4b1a-4c3d-9e8f-7a6b5c4d3e2f\"", "loaded from copy"},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("%v", c.args), func(t *testing.T) {
			origExit := osExit
			defer func() { osExit = origExit }()

			exitCode := 0
			osExit = func(code int) {
				exitCode = code
				panic("exit")
			}

			oldStdout := os.Stdout
			oldStderr := os.Stderr
			rOut, wOut, _ := os.Pipe()
			rErr, wErr, _ := os.Pipe()
			os.Stdout = wOut
			os.Stderr = wErr

			func() {
				defer func() {
					if r := recover(); r != nil {
						if r != "exit" {
							panic(r)
						}
					}
				}()
				cmdManifest(c.args)
			}()

			wOut.Close()
			wErr.Close()
			os.Stdout = oldStdout
			os.Stderr = oldStderr

			var outBuf bytes.Buffer
			var errBuf bytes.Buffer
			outBuf.ReadFrom(rOut)
			errBuf.ReadFrom(rErr)

			if exitCode != c.expected {
				t.Errorf("expected exit code %d, got %d", c.expected, exitCode)
			}
			if c.stdout != "" && !strings.Contains(outBuf.String(), c.stdout) {
				t.Errorf("expected stdout to contain %q, got %q", c.stdout, outBuf.String())
			}
			if c.stderr != "" && !strings.Contains(errBuf.String(), c.stderr) {
				t.Errorf("expected stderr to contain %q, got %q", c.stderr, errBuf.String())
			}
		})
	}
}
