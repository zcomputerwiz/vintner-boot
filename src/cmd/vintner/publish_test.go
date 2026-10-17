// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

func TestCmdPublishNoKernel(t *testing.T) {
	origExit := osExit
	defer func() { osExit = origExit }()

	exitCode := 0
	osExit = func(code int) {
		exitCode = code
		panic("exit")
	}

	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	func() {
		defer func() {
			_ = recover()
		}()
		cmdPublish([]string{})
	}()

	_ = wErr.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rErr)

	if exitCode != 2 {
		t.Fatalf("expected exit code 2, got %d", exitCode)
	}
	if !strings.Contains(buf.String(), "--kernel <abi> is required") {
		t.Fatalf("expected error message about --kernel, got: %s", buf.String())
	}
}

func TestCmdPublishLockContention(t *testing.T) {
	tmp := t.TempDir()
	lockPath := filepath.Join(tmp, "publish.lock")

	lock, err := publish.AcquireLock(lockPath)
	if err != nil {
		t.Fatalf("acquire initial lock: %v", err)
	}
	defer lock.Close()

	bootStorage := filepath.Join(tmp, "bootstorage")
	_ = os.MkdirAll(bootStorage, 0755)

	rootDir := filepath.Join(tmp, "root")
	_ = os.MkdirAll(filepath.Join(rootDir, "boot"), 0755)
	_ = os.WriteFile(filepath.Join(rootDir, "boot", "vmlinuz-6.12.0-amd64"), []byte("vmlinuz"), 0644)
	_ = os.WriteFile(filepath.Join(rootDir, "boot", "initrd.img-6.12.0-amd64"), []byte("initrd"), 0644)

	cmdlineFile := filepath.Join(tmp, "cmdline")
	_ = os.WriteFile(cmdlineFile, []byte("vintner.bootstorage=PARTUUID=12345678-01\n"), 0644)

	origExit := osExit
	defer func() { osExit = origExit }()

	exitCode := 0
	osExit = func(code int) {
		exitCode = code
		panic("exit")
	}

	oldStderr := os.Stderr
	rErr, wErr, _ := os.Pipe()
	os.Stderr = wErr

	func() {
		defer func() {
			_ = recover()
		}()
		cmdPublish([]string{
			"--kernel", "6.12.0-amd64",
			"--boot-storage", bootStorage,
			"--root-dir", rootDir,
			"--cmdline-file", cmdlineFile,
			"--lock-file", lockPath,
		})
	}()

	_ = wErr.Close()
	os.Stderr = oldStderr

	var buf bytes.Buffer
	_, _ = io.Copy(&buf, rErr)

	if exitCode != 1 {
		t.Fatalf("expected exit code 1, got %d", exitCode)
	}
	if !strings.Contains(buf.String(), "another vintner process is running") {
		t.Fatalf("expected lock error message, got: %s", buf.String())
	}
}
