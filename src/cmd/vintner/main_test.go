package main

import (
	"bytes"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestMainDispatch(t *testing.T) {
	origArgs := os.Args
	defer func() { os.Args = origArgs }()

	origExit := osExit
	defer func() { osExit = origExit }()

	cases := []struct {
		args     []string
		expected int
		stdout   string
		stderr   string
	}{
		{[]string{"vintner"}, 2, "", "usage:"},
		{[]string{"vintner", "help"}, 0, "", "usage:"},
		{[]string{"vintner", "-h"}, 0, "", "usage:"},
		{[]string{"vintner", "--help"}, 0, "", "usage:"},
		{[]string{"vintner", "unknown"}, 2, "", "vintner: unknown command"},
	}

	for _, c := range cases {
		t.Run(fmt.Sprintf("%v", c.args), func(t *testing.T) {
			os.Args = c.args

			exitCode := 0
			osExit = func(code int) {
				exitCode = code
				panic("exit") // Prevent further execution in main()
			}

			// Capture stdout and stderr
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
				main()
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
				t.Errorf("expected exit code %d, got %d\nargs: %v", c.expected, exitCode, c.args)
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
