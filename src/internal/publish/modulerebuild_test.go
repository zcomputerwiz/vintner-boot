// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
)

// A DKMS build, rebuild or removal changes the module tree under /lib/modules,
// and the image's record then no longer says what it runs with (WO-0010 5.3).

const modABI = "6.12.111-dkms"

type dkmsEnv struct {
	rootDir, bootStorageDir, stateDir string
	opts                              Options
	out                               *bytes.Buffer
}

// newDKMSEnv publishes generation 2 for modABI, with its module tree recorded.
func newDKMSEnv(t *testing.T) *dkmsEnv {
	t.Helper()
	rootDir, bootStorageDir, m := setupTestEnv(t, "both")
	setupKernelFiles(t, rootDir, modABI)
	e := &dkmsEnv{rootDir: rootDir, bootStorageDir: bootStorageDir, stateDir: filepath.Join(bootStorageDir, "vintner", "state"), out: &bytes.Buffer{}}
	e.opts = Options{
		KernelABI:        modABI,
		BootStorageDir:   bootStorageDir,
		RootDir:          rootDir,
		LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
		CmdlineContent:   m.Images[0].Cmdline,
		PackageQuery:     mockPackageQuery{pkg: "linux-image-" + modABI, ver: "6.12.111-1"},
		UKIBuilder:       mockUKIBuilder{},
		DiskSpaceChecker: mockDiskChecker{free: 100 * 1024 * 1024},
		Stdout:           e.out,
		Stderr:           e.out,
	}
	if res, err := Publish(context.Background(), e.opts); err != nil || res.Unchanged || res.Generation != 2 {
		t.Fatalf("first publish: res=%+v err=%v", res, err)
	}
	e.out.Reset()
	return e
}

func (e *dkmsEnv) publish(t *testing.T) *Result {
	t.Helper()
	e.out.Reset()
	res, err := Publish(context.Background(), e.opts)
	if err != nil {
		t.Fatalf("publish: %v\n%s", err, e.out.String())
	}
	return res
}

func (e *dkmsEnv) manifest(t *testing.T) *manifest.Manifest {
	t.Helper()
	m, err := manifest.Load(e.stateDir)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func image(m *manifest.Manifest, gen int) *manifest.Image {
	for i := range m.Images {
		if m.Images[i].Generation == gen {
			return &m.Images[i]
		}
	}
	return nil
}

// bless makes generation gen GOOD, as the first good boot does.
func (e *dkmsEnv) bless(t *testing.T, gen int) {
	t.Helper()
	m := e.manifest(t)
	img := image(m, gen)
	good := "2026-11-14T12:00:00Z"
	img.State, img.FirstGood, img.LastGood = "GOOD", &good, &good
	if err := manifest.Save(e.stateDir, m); err != nil {
		t.Fatal(err)
	}
}

func (e *dkmsEnv) dkmsModule(t *testing.T, name, content string) {
	t.Helper()
	dir := filepath.Join(e.rootDir, "lib", "modules", modABI, "updates", "dkms")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestPublishDKMSChangeOfAGoodImageMakesANewGeneration(t *testing.T) {
	e := newDKMSEnv(t)
	e.bless(t, 2)
	e.dkmsModule(t, "vendor.ko.xz", "module built by dkms")

	res := e.publish(t)
	if res.Unchanged || res.Generation != 3 {
		t.Fatalf("res %+v, want generation 3 published\n%s", res, e.out.String())
	}
	if !strings.Contains(e.out.String(), "module tree changed (dkms) since generation 2 was published: publishing a new generation") {
		t.Errorf("output does not say why:\n%s", e.out.String())
	}
	m := e.manifest(t)
	want, _ := ComputeDKMSSHA256(filepath.Join(e.rootDir, "lib", "modules", modABI, "updates", "dkms"))
	if g3 := image(m, 3); g3 == nil || g3.State != "TRYING" || g3.ModuleTree == nil || g3.ModuleTree.DKMSSHA256 != want {
		t.Errorf("generation 3 %+v, want TRYING with the current dkms hash %s", g3, want)
	}
	if g2 := image(m, 2); g2 == nil || !g2.Stale {
		t.Errorf("generation 2 %+v, want stale: it predates the module", g2)
	}

	// Nothing changed since: no generation per publish.
	if res := e.publish(t); !res.Unchanged || res.Generation != 3 {
		t.Fatalf("second publish %+v, want unchanged", res)
	}
}

func TestPublishModulesDepChangeOfAGoodImageMakesANewGeneration(t *testing.T) {
	e := newDKMSEnv(t)
	e.bless(t, 2)
	// A module added or removed: depmod rewrites modules.dep.
	dep := filepath.Join(e.rootDir, "lib", "modules", modABI, "modules.dep")
	if err := os.WriteFile(dep, []byte("# dep with another module"), 0o644); err != nil {
		t.Fatal(err)
	}
	res := e.publish(t)
	if res.Unchanged || res.Generation != 3 {
		t.Fatalf("res %+v, want generation 3 published\n%s", res, e.out.String())
	}
	if !strings.Contains(e.out.String(), "module tree changed (modules.dep)") {
		t.Errorf("output does not say why:\n%s", e.out.String())
	}
}

func TestPublishDKMSRemovalOfAGoodImageMakesANewGeneration(t *testing.T) {
	e := newDKMSEnv(t)
	e.bless(t, 2)
	e.dkmsModule(t, "vendor.ko.xz", "module built by dkms")
	if res := e.publish(t); res.Unchanged || res.Generation != 3 {
		t.Fatalf("publish after the build: %+v", res)
	}
	e.bless(t, 3)
	// dkms remove: the file goes, depmod runs.
	if err := os.RemoveAll(filepath.Join(e.rootDir, "lib", "modules", modABI, "updates")); err != nil {
		t.Fatal(err)
	}
	res := e.publish(t)
	if res.Unchanged || res.Generation != 4 || !strings.Contains(e.out.String(), "module tree changed (dkms)") {
		t.Fatalf("res %+v, want generation 4 published for the removal\n%s", res, e.out.String())
	}
}

// A trial that has not booted good yet is still the newest image and nothing
// falls back to it: it takes the new tree instead of leaving a trial of a trial
// for each DKMS call of one package transaction.
func TestPublishDKMSChangeOfAnUnprovenTrialRecordsTheTree(t *testing.T) {
	e := newDKMSEnv(t) // generation 2 is TRYING
	e.dkmsModule(t, "one.ko.xz", "first module")
	res := e.publish(t)
	if !res.Unchanged || res.Generation != 2 {
		t.Fatalf("res %+v, want generation 2 kept\n%s", res, e.out.String())
	}
	if !strings.Contains(e.out.String(), "generation 2 has not booted good yet") {
		t.Errorf("output does not say what happened:\n%s", e.out.String())
	}
	e.dkmsModule(t, "two.ko.xz", "second module")
	e.publish(t)

	m := e.manifest(t)
	if image(m, 3) != nil {
		t.Fatalf("a trial of a trial was published: %+v", m.Images)
	}
	want, _ := ComputeDKMSSHA256(filepath.Join(e.rootDir, "lib", "modules", modABI, "updates", "dkms"))
	if g2 := image(m, 2); g2.ModuleTree == nil || g2.ModuleTree.DKMSSHA256 != want || g2.Stale {
		t.Errorf("generation 2 %+v, want the current dkms hash %s and not stale", g2, want)
	}
}

func TestTreeDifference(t *testing.T) {
	cur := &manifest.ModuleTree{KernelSHA256: "k", ModulesDepSHA256: "dep", DKMSSHA256: "dkms"}
	cases := []struct {
		name string
		rec  *manifest.ModuleTree
		cur  *manifest.ModuleTree
		want string
	}{
		{"same", &manifest.ModuleTree{ModulesDepSHA256: "dep", DKMSSHA256: "dkms"}, cur, ""},
		{"dkms", &manifest.ModuleTree{ModulesDepSHA256: "dep", DKMSSHA256: "old"}, cur, "dkms"},
		{"modules.dep", &manifest.ModuleTree{ModulesDepSHA256: "old", DKMSSHA256: "dkms"}, cur, "modules.dep"},
		{"both", &manifest.ModuleTree{ModulesDepSHA256: "old", DKMSSHA256: "old"}, cur, "dkms, modules.dep"},
		// The installer records no tree in generation 1: it differs only if DKMS modules exist.
		{"no record, no dkms", nil, &manifest.ModuleTree{ModulesDepSHA256: "dep", DKMSSHA256: emptyDKMSSHA256}, ""},
		{"no record, dkms", nil, cur, "dkms"},
	}
	for _, c := range cases {
		if got := treeDifference(c.cur, c.rec); got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
	}
}

func TestStaleReports(t *testing.T) {
	e := newDKMSEnv(t) // generation 2 records the tree; generation 1 has none and another kernel
	m := e.manifest(t)
	if r := StaleReports(m, e.rootDir); len(r) != 0 {
		t.Fatalf("nothing changed, got %q", r)
	}

	e.dkmsModule(t, "vendor.ko.xz", "module built by dkms")
	r := StaleReports(m, e.rootDir)
	if len(r) != 1 || !strings.Contains(r[0], "generation 2 is out of date") || !strings.Contains(r[0], "(dkms)") ||
		!strings.Contains(r[0], "vintner publish --kernel "+modABI) {
		t.Fatalf("reports %q", r)
	}

	// Once a newer image of the kernel records the tree, the older one is not
	// reported: it is out of date for good, and the newer one says what is current.
	e.bless(t, 2)
	if res := e.publish(t); res.Generation != 3 {
		t.Fatalf("publish: %+v", res)
	}
	if r := StaleReports(e.manifest(t), e.rootDir); len(r) != 0 {
		t.Errorf("reports after the publish: %q", r)
	}

	// A kernel that is gone has no tree to compare: publish flags that, not this.
	if err := os.Remove(filepath.Join(e.rootDir, "boot", "vmlinuz-"+modABI)); err != nil {
		t.Fatal(err)
	}
	if r := StaleReports(e.manifest(t), e.rootDir); len(r) != 0 {
		t.Errorf("reports for a removed kernel: %q", r)
	}
}
