// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// vintner install selects the boot screen (WO-0010 D-E, 2.3): Debian's plymouth
// owns plymouthd.conf, so it is an edit that keeps every other line, and the
// initramfs, which carries the file, is built again.

const debianPlymouthConf = "# Distribution configuration is stored in /usr/share/plymouth/plymouthd.defaults\n" +
	"# Administrator customizations go in this file\n" +
	"#[Daemon]\n" +
	"#Theme=bgrt\n" +
	"#ShowDelay=5\n"

// plymouthTarget installs a theme in the target and, with conf, an
// administrator's plymouthd.conf from before the initramfs was built.
func plymouthTarget(t *testing.T, target, theme, conf string) string {
	t.Helper()
	dir := filepath.Join(target, "usr/share/plymouth/themes", theme)
	os.MkdirAll(dir, 0o755)
	os.WriteFile(filepath.Join(dir, theme+".plymouth"), []byte("[Plymouth Theme]\nName="+theme+"\n"), 0o644)
	p := filepath.Join(target, "etc/plymouth/plymouthd.conf")
	if conf != "" {
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(conf), 0o644)
		old := time.Now().Add(-48 * time.Hour)
		os.Chtimes(p, old, old)
	}
	return p
}

// plymouthEnv is fullEnv with an update-initramfs that works: it records how
// it was run and leaves a newer initramfs.
func plymouthEnv(t *testing.T) (*env, FullOptions, *bytes.Buffer, *[][]string) {
	t.Helper()
	e, sr, f, log := fullEnv(t, "uefi")
	ran := &[][]string{}
	f.Runner = runnerFunc(func(ctx context.Context, name string, args ...string) ([]byte, error) {
		if name == "chroot" && len(args) > 1 && args[1] == "update-initramfs" {
			*ran = append(*ran, append([]string{name}, args...))
			p := filepath.Join(e.target, "boot/initrd.img-6.12.9-amd64")
			return nil, os.WriteFile(p, []byte("rebuilt"), 0o644)
		}
		return sr.Run(ctx, name, args...)
	})
	return e, f, log, ran
}

func TestSetDaemonKeys(t *testing.T) {
	kv := [][2]string{{"Theme", "vintner-bgrt"}, {"ShowDelay", "0"}, {"DeviceTimeout", "8"}}
	cases := []struct{ name, in, want string }{
		{"debian file, every line a comment", debianPlymouthConf,
			"# Distribution configuration is stored in /usr/share/plymouth/plymouthd.defaults\n# Administrator customizations go in this file\n#[Daemon]\n#Theme=bgrt\n#ShowDelay=5\n\n[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n"},
		{"empty", "", "[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n"},
		{"keys replaced in place, the rest kept",
			"[Daemon]\nTheme = spinner\nShowDelay=5\nDeviceScale=2\n\n[Other]\nTheme=keep\n",
			"[Daemon]\nDeviceTimeout=8\nTheme=vintner-bgrt\nShowDelay=0\nDeviceScale=2\n\n[Other]\nTheme=keep\n"},
		{"a commented key is not the key", "[Daemon]\n#Theme=spinner\n",
			"[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n#Theme=spinner\n"},
		{"already set", "[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n",
			"[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n"},
	}
	for _, c := range cases {
		got := setDaemonKeys(c.in, kv)
		if got != c.want {
			t.Errorf("%s:\n%q\nwant\n%q", c.name, got, c.want)
		}
		if again := setDaemonKeys(got, kv); again != got {
			t.Errorf("%s: not idempotent:\n%q", c.name, again)
		}
	}
}

func TestInstallSelectsTheDefaultBootScreenAndBuildsTheInitramfsAgain(t *testing.T) {
	e, f, log, ran := plymouthEnv(t)
	p := plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	got := string(read(t, p))
	for _, want := range []string{"\nTheme=vintner-bgrt\n", "\nShowDelay=0\n", "\nDeviceTimeout=8\n", "#Theme=bgrt\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("plymouthd.conf lacks %q:\n%s", want, got)
		}
	}
	if !strings.Contains(log.String(), "Theme=vintner-bgrt") {
		t.Errorf("the log does not say what was changed:\n%s", log)
	}
	// The initramfs carries plymouthd.conf: without a rebuild the screen would not change.
	if len(*ran) != 1 || strings.Join((*ran)[0][3:], " ") != "-u -k 6.12.9-amd64" {
		t.Errorf("update-initramfs ran as %v, want once for the kernel of generation 1", *ran)
	}
}

// Whether the initramfs looks fresh by its time stamp must not matter.
func TestInstallRebuildsEvenWhenTheTimeStampsLookFresh(t *testing.T) {
	e, f, log, ran := plymouthEnv(t)
	plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
	future := time.Now().Add(48 * time.Hour)
	os.Chtimes(filepath.Join(e.target, "boot/initrd.img-6.12.9-amd64"), future, future)
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if len(*ran) != 1 {
		t.Errorf("update-initramfs ran %d times, want 1: a changed plymouthd.conf needs one", len(*ran))
	}
}

func TestInstallLeavesTheInitramfsAloneWhenTheBootScreenWasSelected(t *testing.T) {
	e, f, log, ran := plymouthEnv(t)
	conf := "[Daemon]\nTheme=vintner-bgrt\nShowDelay=0\nDeviceTimeout=8\n"
	p := plymouthTarget(t, e.target, DefaultPlymouthTheme, conf)
	before, _ := os.Stat(p)
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if after, _ := os.Stat(p); !after.ModTime().Equal(before.ModTime()) {
		t.Error("plymouthd.conf was written although it already selects the theme")
	}
	if len(*ran) != 0 {
		t.Errorf("update-initramfs ran: %v", *ran)
	}
	if !strings.Contains(log.String(), "already selects vintner-bgrt") {
		t.Errorf("the log does not say so:\n%s", log)
	}
}

func TestInstallWithoutPlymouthLeavesTheBootScreenAlone(t *testing.T) {
	e, _, f, log := fullEnv(t, "uefi")
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	if exists(filepath.Join(e.target, "etc/plymouth/plymouthd.conf")) {
		t.Error("plymouthd.conf was written for a target without the theme")
	}
	if !strings.Contains(log.String(), "no Plymouth theme vintner-bgrt in the target") {
		t.Errorf("the log does not say so:\n%s", log)
	}
}

func TestInstallPlymouthNoneAndNamedTheme(t *testing.T) {
	t.Run("none", func(t *testing.T) {
		e, f, log, ran := plymouthEnv(t)
		p := plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
		f.PlymouthTheme = PlymouthNone
		if _, err := Install(context.Background(), f); err != nil {
			t.Fatalf("%v\n%s", err, log)
		}
		if string(read(t, p)) != debianPlymouthConf {
			t.Error("plymouthd.conf changed with --plymouth-theme none")
		}
		if len(*ran) != 0 {
			t.Errorf("update-initramfs ran: %v", *ran)
		}
	})
	t.Run("a product's theme", func(t *testing.T) {
		e, f, log, _ := plymouthEnv(t)
		p := plymouthTarget(t, e.target, "wex-boot", debianPlymouthConf)
		f.PlymouthTheme = "wex-boot"
		if _, err := Install(context.Background(), f); err != nil {
			t.Fatalf("%v\n%s", err, log)
		}
		if got := string(read(t, p)); !strings.Contains(got, "\nTheme=wex-boot\n") {
			t.Errorf("plymouthd.conf:\n%s", got)
		}
	})
	t.Run("a theme the target lacks is an error", func(t *testing.T) {
		_, _, f, _ := fullEnv(t, "uefi")
		f.PlymouthTheme = "wex-boot"
		_, err := Install(context.Background(), f)
		var se *Error
		if !errors.As(err, &se) || se.Step != "plymouth" || !strings.Contains(err.Error(), `no Plymouth theme "wex-boot"`) {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("a name that is a path is refused", func(t *testing.T) {
		_, _, f, _ := fullEnv(t, "uefi")
		f.PlymouthTheme = "../../etc"
		if _, err := Install(context.Background(), f); err == nil || !strings.Contains(err.Error(), "not a Plymouth theme name") {
			t.Fatalf("got %v", err)
		}
	})
}

func TestInstallSaysWhatStopsTheBootScreen(t *testing.T) {
	e, f, log, _ := plymouthEnv(t)
	plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
	os.MkdirAll(filepath.Join(e.target, "etc/kernel"), 0o755)
	os.WriteFile(filepath.Join(e.target, "etc/kernel/cmdline"), []byte("# options\nquiet\n"), 0o644)
	os.MkdirAll(filepath.Join(e.target, "etc/systemd/system"), 0o755)
	if err := os.Symlink("/dev/null", filepath.Join(e.target, "etc/systemd/system/plymouth-start.service")); err != nil {
		t.Skipf("symlinks: %v", err)
	}
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatalf("%v\n%s", err, log)
	}
	for _, want := range []string{`no "splash"`, "plymouth-start.service is masked"} {
		if !strings.Contains(log.String(), want) {
			t.Errorf("the log lacks %q:\n%s", want, log)
		}
	}

	// With splash and nothing masked there is nothing to say.
	e, f, log, _ = plymouthEnv(t)
	plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
	os.MkdirAll(filepath.Join(e.target, "etc/kernel"), 0o755)
	os.WriteFile(filepath.Join(e.target, "etc/kernel/cmdline"), []byte("quiet splash\n"), 0o644)
	if _, err := Install(context.Background(), f); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(log.String(), "note:") {
		t.Errorf("a note for a working setup:\n%s", log)
	}
}

// A plymouthd.conf the administrator changed after the initramfs was built is
// an input like a hook: an install that leaves the screen alone still refuses.
func TestNewerPlymouthConfMakesTheInitramfsStale(t *testing.T) {
	e, _, f, _ := fullEnv(t, "uefi")
	f.PlymouthTheme = PlymouthNone
	p := plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
	now := time.Now()
	os.Chtimes(p, now, now)
	old := now.Add(-time.Hour)
	os.Chtimes(filepath.Join(e.target, "boot/initrd.img-6.12.9-amd64"), old, old)
	_, err := Install(context.Background(), f)
	var se *Error
	if !errors.As(err, &se) || se.Step != "initramfs" || !strings.Contains(err.Error(), "plymouthd.conf") {
		t.Fatalf("got %v, want a stale initramfs refusal naming plymouthd.conf", err)
	}
}

// themeNeedsModule makes a theme name its plugin, and installs the plugin or not.
func themeNeedsModule(t *testing.T, target, theme, module string, present bool) {
	t.Helper()
	f := filepath.Join(target, "usr/share/plymouth/themes", theme, theme+".plymouth")
	if err := os.WriteFile(f, []byte("[Plymouth Theme]\nName="+theme+"\nModuleName="+module+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if present {
		dir := filepath.Join(target, "usr/lib/x86_64-linux-gnu/plymouth")
		os.MkdirAll(dir, 0o755)
		os.WriteFile(filepath.Join(dir, module+".so"), []byte("plugin"), 0o644)
	}
}

// Debian's initramfs hook skips Plymouth entirely when the theme's plugin
// (two-step.so, from plymouth-themes) is missing, so no configuration helps.
func TestInstallWithoutTheThemesPluginSaysSoAndChangesNothing(t *testing.T) {
	t.Run("the default theme is left alone, with a note", func(t *testing.T) {
		e, f, log, ran := plymouthEnv(t)
		p := plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
		themeNeedsModule(t, e.target, DefaultPlymouthTheme, "two-step", false)
		if _, err := Install(context.Background(), f); err != nil {
			t.Fatalf("%v\n%s", err, log)
		}
		if string(read(t, p)) != debianPlymouthConf {
			t.Error("plymouthd.conf was changed although the initramfs would skip Plymouth")
		}
		if len(*ran) != 0 {
			t.Errorf("update-initramfs ran for nothing: %v", *ran)
		}
		for _, want := range []string{"two-step.so", "plymouth-themes", "no Plymouth at all"} {
			if !strings.Contains(log.String(), want) {
				t.Errorf("the log lacks %q:\n%s", want, log)
			}
		}
	})
	t.Run("a theme named on purpose is an error", func(t *testing.T) {
		e, f, _, _ := plymouthEnv(t)
		plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
		themeNeedsModule(t, e.target, DefaultPlymouthTheme, "two-step", false)
		f.PlymouthTheme = DefaultPlymouthTheme
		_, err := Install(context.Background(), f)
		var se *Error
		if !errors.As(err, &se) || se.Step != "plymouth" || !strings.Contains(err.Error(), "two-step.so") {
			t.Fatalf("got %v", err)
		}
	})
	t.Run("with the plugin the screen is selected", func(t *testing.T) {
		e, f, log, ran := plymouthEnv(t)
		p := plymouthTarget(t, e.target, DefaultPlymouthTheme, debianPlymouthConf)
		themeNeedsModule(t, e.target, DefaultPlymouthTheme, "two-step", true)
		if _, err := Install(context.Background(), f); err != nil {
			t.Fatalf("%v\n%s", err, log)
		}
		if !strings.Contains(string(read(t, p)), "\nTheme=vintner-bgrt\n") || len(*ran) != 1 {
			t.Errorf("conf:\n%s\nupdate-initramfs ran %d times", read(t, p), len(*ran))
		}
	})
}
