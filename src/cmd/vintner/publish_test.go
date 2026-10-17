// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
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
