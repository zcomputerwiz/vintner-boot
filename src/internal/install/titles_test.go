// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
)

// Recovery's menu titles (WO-0010 4.1): "Recovery Console" and "Recovery Console
// (backup)" until the product sets its own, the same on both firmwares, and no
// visible text says "vintner".
func TestRecoveryTitles(t *testing.T) {
	for _, tc := range []struct {
		name, title, wantA, wantB string
	}{
		{"default", "", "Recovery Console", "Recovery Console (backup)"},
		{"product", "WineExplorer Rescue", "WineExplorer Rescue", "WineExplorer Rescue (backup)"},
		{"quoted", `Rescue "$HOME"`, `Rescue "$HOME"`, `Rescue "$HOME" (backup)`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := newEnv(t, "both")
			e.opts.RecoveryTitle = tc.title
			if _, err := Deploy(context.Background(), e.opts); err != nil {
				t.Fatal(err)
			}
			want := map[string]string{"a": tc.wantA, "b": tc.wantB}
			if len(e.uki.osrels) != 2 {
				t.Fatalf("%d UKI builds", len(e.uki.osrels))
			}
			for i, slot := range []string{"a", "b"} {
				osrel := e.uki.osrels[i]
				pretty := "PRETTY_NAME=" + quoteForOSRelease(want[slot]) + "\n"
				if !strings.Contains(osrel, pretty) {
					t.Errorf("recovery %s .osrel %q lacks %q", slot, osrel, pretty)
				}
				if strings.Contains(strings.ToLower(osrel[strings.Index(osrel, "PRETTY_NAME"):]), "vintner") {
					t.Errorf("recovery %s title says vintner: %q", slot, osrel)
				}
				if !strings.Contains(osrel, "IMAGE_ID=vintner-recovery-"+slot+"\n") {
					t.Errorf("recovery %s lost its sort key: %q", slot, osrel)
				}
				cfg := string(read(t, filepath.Join(e.bs, "vintner/bios/recovery-"+slot, "slot.cfg")))
				if !strings.Contains(cfg, "set vintner_title="+quoteForGrub(want[slot])+"\n") {
					t.Errorf("recovery %s slot.cfg %q lacks the title %q", slot, cfg, want[slot])
				}
			}
		})
	}
}

func TestRecoveryTitleIsValidated(t *testing.T) {
	for _, title := range []string{strings.Repeat("x", 65), "two\nlines"} {
		e := newEnv(t, "uefi")
		e.opts.RecoveryTitle = title
		if _, err := Deploy(context.Background(), e.opts); err == nil || !strings.Contains(err.Error(), "--recovery-title") {
			t.Errorf("title %q: %v", title, err)
		}
	}
}

func quoteForOSRelease(s string) string {
	r := strings.NewReplacer(`"`, `\"`, `\`, `\\`, `$`, `\$`, "`", "\\`")
	return `"` + r.Replace(s) + `"`
}

func quoteForGrub(s string) string {
	r := strings.NewReplacer(`"`, `\"`, `\`, `\\`, `$`, `\$`)
	return `"` + r.Replace(s) + `"`
}
