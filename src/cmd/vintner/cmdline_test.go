// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

// fakePublish replaces the publisher and records what it was asked to do and
// what the options file held at that moment.
type fakePublish struct {
	called  int
	opts    publish.Options
	file    string // the options file as the publisher saw it
	result  publish.Result
	failure error
}

func (f *fakePublish) install(t *testing.T) {
	old := publishFn
	publishFn = func(_ context.Context, o publish.Options) (*publish.Result, error) {
		f.called++
		f.opts = o
		b, _ := os.ReadFile(o.KernelCmdlineFile)
		f.file = string(b)
		if f.failure != nil {
			return nil, f.failure
		}
		r := f.result
		return &r, nil
	}
	t.Cleanup(func() { publishFn = old })
}

func runCmdlineJSON(t *testing.T, args ...string) (int, cmdlineResult) {
	t.Helper()
	var out, errb bytes.Buffer
	code := runCmdline(append([]string{"--json"}, args...), &out, &errb)
	var r cmdlineResult
	if err := json.Unmarshal(out.Bytes(), &r); err != nil {
		t.Fatalf("not one JSON object (%v): %q (stderr %q)", err, out.String(), errb.String())
	}
	return code, r
}

func TestCmdlineShow(t *testing.T) {
	root := t.TempDir()
	proc := filepath.Join(root, "proc-cmdline")
	os.WriteFile(proc, []byte("root=UUID=x ro quiet vintner.bootstorage=PARTUUID=y\n"), 0o644)

	code, r := runCmdlineJSON(t, "show", "--root-dir", root, "--proc-cmdline", proc)
	if code != cmdlineOK || !r.OK || r.Exists == nil || *r.Exists || len(r.Options) != 0 {
		t.Errorf("no file: %d %+v", code, r)
	}
	if r.Running != "root=UUID=x ro quiet vintner.bootstorage=PARTUUID=y" {
		t.Errorf("running %q", r.Running)
	}

	os.MkdirAll(filepath.Join(root, "etc", "kernel"), 0o755)
	os.WriteFile(filepath.Join(root, "etc", "kernel", "cmdline"), []byte("# c\nnomodeset console=ttyS0\n"), 0o644)
	code, r = runCmdlineJSON(t, "show", "--root-dir", root, "--proc-cmdline", proc)
	if code != cmdlineOK || !*r.Exists || strings.Join(r.Options, " ") != "nomodeset console=ttyS0" {
		t.Errorf("a file: %d %+v", code, r)
	}

	os.WriteFile(filepath.Join(root, "etc", "kernel", "cmdline"), []byte("root=/dev/x\n"), 0o644)
	code, r = runCmdlineJSON(t, "show", "--root-dir", root, "--proc-cmdline", proc)
	if code != cmdlineRefused || r.OK || r.Code != "refused" || !strings.Contains(r.Error, "cmdline") {
		t.Errorf("a bad file: %d %+v", code, r)
	}
}

func TestCmdlineSet(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "etc", "kernel", "cmdline")
	f := &fakePublish{result: publish.Result{Generation: 5}}
	f.install(t)

	code, r := runCmdlineJSON(t, "set", "--root-dir", root, "--kernel", "6.12.5-amd64", "--boot-storage", "/bs", "nomodeset", "console=ttyS0,115200")
	if code != cmdlineOK || !r.OK || r.Published == nil || !*r.Published || r.Generation == nil || *r.Generation != 5 {
		t.Fatalf("set: %d %+v", code, r)
	}
	if got, _ := os.ReadFile(file); string(got) != "nomodeset console=ttyS0,115200\n" {
		t.Errorf("file %q", got)
	}
	// The publisher saw the new file, for the given kernel, root and boot storage.
	if f.called != 1 || f.file != "nomodeset console=ttyS0,115200\n" || f.opts.KernelABI != "6.12.5-amd64" ||
		f.opts.RootDir != root || f.opts.BootStorageDir != "/bs" || f.opts.KernelCmdlineFile != file {
		t.Errorf("publish: %d calls, %+v, file %q", f.called, f.opts, f.file)
	}

	// The same options again: published, unchanged.
	f.result = publish.Result{Unchanged: true}
	code, r = runCmdlineJSON(t, "set", "--root-dir", root, "--kernel", "k", "nomodeset", "console=ttyS0,115200")
	if code != cmdlineOK || r.Unchanged == nil || !*r.Unchanged || r.Generation != nil {
		t.Errorf("unchanged: %d %+v", code, r)
	}

	// One argument holding several options is the same as several arguments.
	code, _ = runCmdlineJSON(t, "set", "--root-dir", root, "--kernel", "k", "quiet splash")
	if got, _ := os.ReadFile(file); code != cmdlineOK || string(got) != "quiet splash\n" {
		t.Errorf("one argument: %d %q", code, got)
	}

	// --clear leaves an empty file, which means "no options", not "carry on".
	code, r = runCmdlineJSON(t, "set", "--root-dir", root, "--kernel", "k", "--clear")
	if got, err := os.ReadFile(file); code != cmdlineOK || err != nil || len(got) != 0 {
		t.Errorf("clear: %d %q %v %+v", code, got, err, r)
	}

	// --no-publish writes the file and publishes nothing.
	f.called = 0
	code, r = runCmdlineJSON(t, "set", "--root-dir", root, "--no-publish", "quiet")
	if got, _ := os.ReadFile(file); code != cmdlineOK || f.called != 0 || string(got) != "quiet\n" || r.Published == nil || *r.Published {
		t.Errorf("no-publish: %d, %d calls, %q, %+v", code, f.called, got, r)
	}
}

// What belongs to vintner is refused before anything is written or published.
func TestCmdlineSetRefused(t *testing.T) {
	root := t.TempDir()
	f := &fakePublish{}
	f.install(t)
	for _, bad := range [][]string{{"root=/dev/sda1"}, {"quiet", "vintner.bootstorage=PARTUUID=x"}, {"ro"}, {strings.Repeat("a", 2000)}} {
		code, r := runCmdlineJSON(t, append([]string{"set", "--root-dir", root, "--kernel", "k"}, bad...)...)
		if code != cmdlineRefused || r.OK || r.Code != "refused" {
			t.Errorf("%q: %d %+v", bad[0][:4], code, r)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "etc", "kernel", "cmdline")); err == nil {
		t.Error("a refused option was written")
	}
	if f.called != 0 {
		t.Errorf("the publisher ran %d times", f.called)
	}
	for _, args := range [][]string{{"set"}, {"set", "--clear", "quiet"}, {"frob"}, {}, {"show", "x"}} {
		var out, errb bytes.Buffer
		if code := runCmdline(args, &out, &errb); code != cmdlineUsage {
			t.Errorf("%q: exit %d, want a usage error", args, code)
		}
	}
}

// When publishing fails, the file goes back as it was, so it never differs from
// what was published: the next kernel update would otherwise publish an option
// nobody saw start.
func TestCmdlineSetRestoresOnPublishFailure(t *testing.T) {
	root := t.TempDir()
	file := filepath.Join(root, "etc", "kernel", "cmdline")
	f := &fakePublish{failure: errors.New("no space left")}
	f.install(t)

	// There was no file.
	code, r := runCmdlineJSON(t, "set", "--root-dir", root, "--kernel", "k", "nomodeset")
	if code != cmdlineFailed || r.OK || r.Code != "failed" || !strings.Contains(r.Error, "no space left") {
		t.Errorf("failure: %d %+v", code, r)
	}
	if _, err := os.Stat(file); err == nil {
		t.Error("the file stayed although the publish failed")
	}
	if f.file != "nomodeset\n" {
		t.Errorf("the publisher saw %q", f.file)
	}

	// There was a file.
	os.MkdirAll(filepath.Dir(file), 0o755)
	os.WriteFile(file, []byte("quiet\n"), 0o644)
	runCmdlineJSON(t, "set", "--root-dir", root, "--kernel", "k", "nomodeset")
	if got, _ := os.ReadFile(file); string(got) != "quiet\n" {
		t.Errorf("the file is %q, want its old content", got)
	}
}
