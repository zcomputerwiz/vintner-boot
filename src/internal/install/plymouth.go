// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// DefaultPlymouthTheme is the boot screen vintner-core ships (WO-0010 D-E).
const DefaultPlymouthTheme = "vintner-bgrt"

// PlymouthNone is the value of Options.PlymouthTheme that leaves the target's
// Plymouth configuration alone.
const PlymouthNone = "none"

// plymouthConf is the administrator's Plymouth configuration, which Debian's
// plymouth package ships as a conffile with every line a comment, and which
// initramfs-tools copies into the initramfs (so a change needs a new one).
const plymouthConf = "etc/plymouth/plymouthd.conf"

var plymouthThemeName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]*$`)

// setPlymouth selects the boot screen in the target (WO-0010 D-E, 2.3): Theme,
// ShowDelay=0 and DeviceTimeout=8 in the [Daemon] section of plymouthd.conf, every
// other line kept. vintner-core does not ship the file, because Debian's
// plymouth does, so this is an administrator's edit, which survives a reinstall
// of plymouth. When the target has no Plymouth theme of that name the default
// leaves the screen alone and says so; a theme named on purpose is an error.
func (d *deployer) setPlymouth() error {
	theme := d.PlymouthTheme
	if theme == PlymouthNone {
		return nil
	}
	named := theme != ""
	if !named {
		theme = DefaultPlymouthTheme
	}
	if !plymouthThemeName.MatchString(theme) {
		return d.fail("plymouth", fmt.Errorf("%q is not a Plymouth theme name", theme))
	}
	themeFile := filepath.Join(d.TargetDir, "usr", "share", "plymouth", "themes", theme, theme+".plymouth")
	if _, err := os.Stat(themeFile); err != nil {
		if !os.IsNotExist(err) {
			return d.fail("plymouth", err)
		}
		if named {
			return d.fail("plymouth", fmt.Errorf("the target has no Plymouth theme %q (%s)", theme, themeFile))
		}
		fmt.Fprintf(d.Log, "  no Plymouth theme %s in the target: the boot screen is left as it is (--plymouth-theme none says so on purpose)\n", theme)
		return nil
	}

	// Debian's initramfs hook prints "plymouth module ... missing, skipping plymouth" and
	// builds an initramfs with no Plymouth at all when the theme's plugin is not there:
	// two-step.so, which package plymouth-themes ships and vintner-core only recommends.
	if mod := themeModule(themeFile); mod != "" {
		if found, _ := filepath.Glob(filepath.Join(d.TargetDir, "usr", "lib", "*", "plymouth", mod+".so")); len(found) == 0 {
			why := fmt.Sprintf("the target has no Plymouth plugin %s.so that theme %s needs (Debian: package plymouth-themes), and without it the initramfs is built with no Plymouth at all", mod, theme)
			if named {
				return d.fail("plymouth", errors.New(why))
			}
			fmt.Fprintf(d.Log, "  note: %s: the boot screen is left as it is\n", why)
			return nil
		}
	}

	p := filepath.Join(d.TargetDir, filepath.FromSlash(plymouthConf))
	old, err := os.ReadFile(p)
	if err != nil && !os.IsNotExist(err) {
		return d.fail("plymouth", err)
	}
	conf := setDaemonKeys(string(old), [][2]string{{"Theme", theme}, {"ShowDelay", "0"}, {"DeviceTimeout", "8"}})
	if err == nil && conf == string(old) {
		fmt.Fprintf(d.Log, "  %s already selects %s\n", "/"+plymouthConf, theme)
	} else {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return d.fail("plymouth", err)
		}
		if err := writeFileSync(p, []byte(conf)); err != nil {
			return d.fail("plymouth", err)
		}
		d.rec("set Theme=%s, ShowDelay=0 and DeviceTimeout=8 in /%s", theme, plymouthConf)
		d.plymouthChanged = true
	}
	d.plymouthNotes(theme)
	return nil
}

// plymouthNotes says what else stops the boot screen from showing; it is the
// product's to fix (the command line and the units are its), so these are notes.
func (d *deployer) plymouthNotes(theme string) {
	if b, err := os.ReadFile(filepath.Join(d.TargetDir, "etc", "kernel", "cmdline")); err == nil {
		splash := false
		for _, f := range strings.Fields(strings.Join(nonComment(string(b)), " ")) {
			if f == "splash" {
				splash = true
			}
		}
		if !splash {
			fmt.Fprintf(d.Log, "  note: /etc/kernel/cmdline has no \"splash\", so Plymouth will not start; add it to show %s\n", theme)
		}
	}
	for _, u := range []string{"plymouth-start.service", "plymouth.service"} {
		if dest, err := os.Readlink(filepath.Join(d.TargetDir, "etc", "systemd", "system", u)); err == nil && dest == "/dev/null" {
			fmt.Fprintf(d.Log, "  note: %s is masked in the target, so Plymouth will not start; unmask it to show %s\n", u, theme)
		}
	}
}

// themeModule is the ModuleName of a theme file, or "" when it names none.
func themeModule(themeFile string) string {
	b, err := os.ReadFile(themeFile)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if k, v, ok := strings.Cut(strings.TrimSpace(l), "="); ok && strings.TrimSpace(k) == "ModuleName" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func nonComment(s string) []string {
	var out []string
	for _, l := range strings.Split(s, "\n") {
		if t := strings.TrimSpace(l); t != "" && !strings.HasPrefix(t, "#") {
			out = append(out, t)
		}
	}
	return out
}

// setDaemonKeys sets keys in the [Daemon] section of a keyfile, replacing a key
// that is set there and adding the others right after the section header, and
// keeps every other line. A file with no [Daemon] section gets one.
func setDaemonKeys(conf string, kv [][2]string) string {
	lines := strings.Split(conf, "\n")
	section := ""
	daemon := -1 // index of the [Daemon] header line
	done := map[string]bool{}
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if strings.HasPrefix(t, "[") && strings.HasSuffix(t, "]") {
			section = t
			if t == "[Daemon]" && daemon < 0 {
				daemon = i
			}
			continue
		}
		if section != "[Daemon]" || strings.HasPrefix(t, "#") || strings.HasPrefix(t, ";") {
			continue
		}
		key, _, ok := strings.Cut(t, "=")
		if !ok {
			continue
		}
		key = strings.TrimSpace(key)
		for _, p := range kv {
			if key == p[0] {
				lines[i] = p[0] + "=" + p[1]
				done[key] = true
			}
		}
	}
	var add []string
	for _, p := range kv {
		if !done[p[0]] {
			add = append(add, p[0]+"="+p[1])
		}
	}
	if len(add) == 0 {
		return strings.Join(lines, "\n")
	}
	if daemon < 0 {
		out := strings.TrimRight(conf, "\n")
		if out != "" {
			out += "\n\n"
		}
		return out + "[Daemon]\n" + strings.Join(add, "\n") + "\n"
	}
	out := append([]string(nil), lines[:daemon+1]...)
	out = append(out, add...)
	return strings.Join(append(out, lines[daemon+1:]...), "\n")
}
