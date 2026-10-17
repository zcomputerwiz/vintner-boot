// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
)

func writeTestPE(path string, osrel, cmdline, uname, initrd []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dosHeader := make([]byte, 64)
	dosHeader[0] = 'M'
	dosHeader[1] = 'Z'
	binary.LittleEndian.PutUint32(dosHeader[60:], 64)
	if _, err := f.Write(dosHeader); err != nil {
		return err
	}

	if _, err := f.Write([]byte("PE\x00\x00")); err != nil {
		return err
	}

	fileHeader := make([]byte, 20)
	binary.LittleEndian.PutUint16(fileHeader[0:], 0x8664)

	type section struct {
		name string
		data []byte
	}
	var sections []section
	if osrel != nil {
		sections = append(sections, section{".osrel", osrel})
	}
	if cmdline != nil {
		sections = append(sections, section{".cmdline", cmdline})
	}
	if uname != nil {
		sections = append(sections, section{".uname", uname})
	}
	if initrd != nil {
		sections = append(sections, section{".initrd", initrd})
	}

	binary.LittleEndian.PutUint16(fileHeader[2:], uint16(len(sections)))
	optHeaderSize := uint16(240)
	binary.LittleEndian.PutUint16(fileHeader[16:], optHeaderSize)
	binary.LittleEndian.PutUint16(fileHeader[18:], 0x0200)
	if _, err := f.Write(fileHeader); err != nil {
		return err
	}

	optHeader := make([]byte, optHeaderSize)
	binary.LittleEndian.PutUint16(optHeader[0:], 0x20b)
	binary.LittleEndian.PutUint32(optHeader[108:], 16)
	if _, err := f.Write(optHeader); err != nil {
		return err
	}

	var offset uint32 = 64 + 4 + 20 + uint32(optHeaderSize) + uint32(len(sections)*40)
	for _, sec := range sections {
		secHeader := make([]byte, 40)
		copy(secHeader[0:8], sec.name)
		size := uint32(len(sec.data))
		binary.LittleEndian.PutUint32(secHeader[8:], size)
		binary.LittleEndian.PutUint32(secHeader[12:], offset)
		binary.LittleEndian.PutUint32(secHeader[16:], size)
		binary.LittleEndian.PutUint32(secHeader[20:], offset)
		if _, err := f.Write(secHeader); err != nil {
			return err
		}
		offset += size
	}

	for _, sec := range sections {
		if _, err := f.Write(sec.data); err != nil {
			return err
		}
	}
	return nil
}

type mockUKIBuilder struct {
	fail bool
}

func (m mockUKIBuilder) Build(ctx context.Context, req UKIRequest) error {
	if m.fail {
		return fmt.Errorf("mock ukify failure")
	}
	osrelData, _ := os.ReadFile(req.OSReleasePath)
	initrdData, _ := os.ReadFile(req.InitrdPath)
	return writeTestPE(req.OutputPath, osrelData, []byte(req.Cmdline), []byte(req.Uname), initrdData)
}

type mockPackageQuery struct {
	pkg  string
	ver  string
	fail bool
}

func (m mockPackageQuery) PackageAndVersion(vmlinuzPath string) (string, string, error) {
	if m.fail {
		return "", "", fmt.Errorf("mock dpkg failure: no package owns %s", vmlinuzPath)
	}
	return m.pkg, m.ver, nil
}

type mockClock struct {
	t time.Time
}

func (m mockClock) Now() time.Time {
	return m.t
}

type mockDiskChecker struct {
	free uint64
}

func (m mockDiskChecker) FreeBytes(path string) (uint64, error) {
	return m.free, nil
}

func setupTestEnv(t *testing.T, fw string) (rootDir, bootStorageDir string, m *manifest.Manifest) {
	tmpDir := t.TempDir()
	rootDir = filepath.Join(tmpDir, "root")
	bootStorageDir = filepath.Join(tmpDir, "bootstorage")

	if err := os.MkdirAll(filepath.Join(rootDir, "boot"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "etc", "apt", "apt.conf.d"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(rootDir, "usr", "lib", "systemd", "boot", "efi"), 0755); err != nil {
		t.Fatal(err)
	}
	stubFile := filepath.Join(rootDir, "usr", "lib", "systemd", "boot", "efi", "linuxx64.efi.stub")
	if err := os.WriteFile(stubFile, []byte("stub"), 0644); err != nil {
		t.Fatal(err)
	}

	osRelease := filepath.Join(rootDir, "etc", "os-release")
	osReleaseContent := "PRETTY_NAME=\"Debian GNU/Linux 13 (trixie)\"\nNAME=\"Debian GNU/Linux\"\nVERSION_ID=\"13\"\n"
	if err := os.WriteFile(osRelease, []byte(osReleaseContent), 0644); err != nil {
		t.Fatal(err)
	}

	stateDir := filepath.Join(bootStorageDir, "vintner", "state")
	if err := os.MkdirAll(stateDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bootStorageDir, "EFI", "Linux"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bootStorageDir, "vintner", "bios", "os-1"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(bootStorageDir, "grub"), 0755); err != nil {
		t.Fatal(err)
	}

	// Create grubenv
	grubenvPath := filepath.Join(bootStorageDir, "grub", "grubenv")
	env := &grubenv.Env{}
	env.Set("VINTNER_ORDER", "1")
	env.Set("VINTNER_T_1", "xxx")
	if err := os.WriteFile(grubenvPath, make([]byte, 1024), 0644); err != nil {
		t.Fatal(err)
	}
	if err := grubenv.Write(grubenvPath, env); err != nil {
		t.Fatal(err)
	}

	rootUUID := "00000000-0000-0000-0000-000000000001"
	rootPartUUID := "00000000-0000-0000-0000-000000000002"
	bsPartUUID := "00000000-0000-0000-0000-000000000003"
	cmdline := fmt.Sprintf("root=UUID=%s ro vintner.bootstorage=PARTUUID=%s", rootUUID, bsPartUUID)

	pkg1 := "linux-image-6.12-base"
	ver1 := "6.12-1"
	m = &manifest.Manifest{
		Format:         1,
		Seq:            1,
		InstallationID: "11111111-1111-1111-1111-111111111111",
		MachineID:      "22222222222222222222222222222222",
		Firmware:       fw,
		SecureBootTier: "none",
		Root: manifest.Root{
			UUID:     rootUUID,
			PartUUID: rootPartUUID,
		},
		BootStorage: manifest.BootStorage{
			PartUUID: bsPartUUID,
		},
		Tries:          3,
		Retain:         3,
		NextGeneration: 2,
		Images: []manifest.Image{
			{
				Generation: 1,
				State:      "GOOD",
				Stale:      false,
				Kernel: manifest.Kernel{
					ABI:     "6.12-base",
					Package: &pkg1,
					Version: &ver1,
				},
				Cmdline: cmdline,
				Created: "2026-10-15T00:00:00Z",
			},
		},
		Recovery: []manifest.Recovery{},
	}

	if fw == "uefi" || fw == "both" {
		ukiPath := filepath.Join(bootStorageDir, "EFI", "Linux", "vintner-os-1+3-0.efi")
		_ = writeTestPE(ukiPath, []byte("osrel"), []byte(cmdline), []byte("6.12-base"), []byte("initrd1"))
		fi, _ := os.Stat(ukiPath)
		h, _ := fileSHA256(ukiPath)
		m.Images[0].UEFI = &manifest.UEFIFile{
			Entry:  "vintner-os-1",
			SHA256: h,
			Size:   int(fi.Size()),
		}
	}
	if fw == "bios" || fw == "both" {
		slotDir := filepath.Join(bootStorageDir, "vintner", "bios", "os-1")
		_ = os.WriteFile(filepath.Join(slotDir, "vmlinuz"), []byte("vmlinuz1"), 0644)
		_ = os.WriteFile(filepath.Join(slotDir, "initrd.img"), []byte("initrd1"), 0644)
		_ = os.WriteFile(filepath.Join(slotDir, "slot.cfg"), []byte("set vintner_cmdline=\""+cmdline+"\"\n"), 0644)
		vH, _ := fileSHA256(filepath.Join(slotDir, "vmlinuz"))
		iH, _ := fileSHA256(filepath.Join(slotDir, "initrd.img"))
		sH, _ := fileSHA256(filepath.Join(slotDir, "slot.cfg"))
		m.Images[0].BIOS = &manifest.BIOSFiles{
			Dir: "/vintner/bios/os-1",
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   vH,
				InitrdImg: iH,
				SlotCfg:   sH,
			},
		}
	}

	if err := manifest.Save(stateDir, m); err != nil {
		t.Fatal(err)
	}

	return rootDir, bootStorageDir, m
}

func setupKernelFiles(t *testing.T, rootDir, abi string) {
	if err := os.MkdirAll(filepath.Join(rootDir, "boot"), 0755); err != nil {
		t.Fatal(err)
	}
	vmlinuzPath := filepath.Join(rootDir, "boot", "vmlinuz-"+abi)
	initrdPath := filepath.Join(rootDir, "boot", "initrd.img-"+abi)
	if err := os.WriteFile(vmlinuzPath, []byte("vmlinuz-content-"+abi), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(initrdPath, []byte("initrd-content-"+abi), 0644); err != nil {
		t.Fatal(err)
	}

	modDir := filepath.Join(rootDir, "lib", "modules", abi)
	if err := os.MkdirAll(modDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(modDir, "modules.dep"), []byte("# dep"), 0644); err != nil {
		t.Fatal(err)
	}
}

func snapshotDir(t *testing.T, dir string) map[string]string {
	res := make(map[string]string)
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			rel, _ := filepath.Rel(dir, path)
			h, _ := fileSHA256(path)
			res[rel] = h
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestPublishFull(t *testing.T) {
	for _, fw := range []string{"uefi", "bios", "both"} {
		t.Run("Firmware_"+fw, func(t *testing.T) {
			rootDir, bootStorageDir, m := setupTestEnv(t, fw)
			newABI := "6.12.111-amd64"
			setupKernelFiles(t, rootDir, newABI)

			pkgQuery := mockPackageQuery{pkg: "linux-image-" + newABI, ver: "6.12.111-1"}
			ukiB := mockUKIBuilder{}
			clk := mockClock{t: time.Date(2026, 10, 16, 12, 0, 0, 0, time.UTC)}
			diskCheck := mockDiskChecker{free: 100 * 1024 * 1024}

			var stdout bytes.Buffer
			opts := Options{
				KernelABI:        newABI,
				BootStorageDir:   bootStorageDir,
				RootDir:          rootDir,
				LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
				CmdlineContent:   m.Images[0].Cmdline,
				PackageQuery:     pkgQuery,
				UKIBuilder:       ukiB,
				DiskSpaceChecker: diskCheck,
				Clock:            clk,
				Stdout:           &stdout,
			}

			res, err := Publish(context.Background(), opts)
			if err != nil {
				t.Fatalf("Publish failed: %v", err)
			}
			if res.Unchanged {
				t.Errorf("expected new publish, got unchanged")
			}
			if res.Generation != 2 {
				t.Errorf("expected generation 2, got %d", res.Generation)
			}

			// Verify manifest
			stateDir := filepath.Join(bootStorageDir, "vintner", "state")
			loaded, err := manifest.Load(stateDir)
			if err != nil {
				t.Fatalf("manifest.Load: %v", err)
			}
			if loaded.NextGeneration != 3 {
				t.Errorf("NextGeneration: got %d, want 3", loaded.NextGeneration)
			}
			if len(loaded.Images) != 2 {
				t.Fatalf("Images count: got %d, want 2", len(loaded.Images))
			}
			gen2 := loaded.Images[1]
			if gen2.Generation != 2 || gen2.State != "TRYING" {
				t.Errorf("Image 2 invalid: %+v", gen2)
			}

			// Verify firmware files
			if fw == "uefi" || fw == "both" {
				ukiPath := filepath.Join(bootStorageDir, "EFI", "Linux", "vintner-os-2+3-0.efi")
				if _, err := os.Stat(ukiPath); err != nil {
					t.Errorf("expected published UKI %s: %v", ukiPath, err)
				}
			}
			if fw == "bios" || fw == "both" {
				slotDir := filepath.Join(bootStorageDir, "vintner", "bios", "os-2")
				for _, f := range []string{"vmlinuz", "initrd.img", "slot.cfg"} {
					if _, err := os.Stat(filepath.Join(slotDir, f)); err != nil {
						t.Errorf("expected BIOS file %s: %v", f, err)
					}
				}
				env, err := grubenv.Read(filepath.Join(bootStorageDir, "grub", "grubenv"))
				if err != nil {
					t.Fatal(err)
				}
				order, _ := env.Get("VINTNER_ORDER")
				if order != "2 1" {
					t.Errorf("VINTNER_ORDER: got %q, want \"2 1\"", order)
				}
				t2, _ := env.Get("VINTNER_T_2")
				if t2 != "xxx" {
					t.Errorf("VINTNER_T_2: got %q, want \"xxx\"", t2)
				}
			}

			// Verify staging is empty
			stgDir := filepath.Join(bootStorageDir, "vintner", "staging")
			entries, _ := os.ReadDir(stgDir)
			if len(entries) != 0 {
				t.Errorf("staging not empty: %d entries", len(entries))
			}

			// Verify NeverAutoRemove
			retainedPath := filepath.Join(rootDir, "etc", "apt", "apt.conf.d", "50vintner-retained-kernels")
			retBytes, err := os.ReadFile(retainedPath)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(retBytes), `linux-image-6\.12\.111-amd64`) {
				t.Errorf("NeverAutoRemove missing image 2 package: %s", string(retBytes))
			}
		})
	}
}

func TestPublishRefusals(t *testing.T) {
	// Step 1: missing vmlinuz
	t.Run("Refusal_Step1_MissingVmlinuz", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "both")
		beforeSnap := snapshotDir(t, bootStorageDir)

		initrdPath := filepath.Join(rootDir, "boot", "initrd.img-test")
		_ = os.WriteFile(initrdPath, []byte("initrd"), 0644)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			CmdlineContent: m.Images[0].Cmdline,
		}
		_, err := Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected refusal on missing vmlinuz")
		}
		afterSnap := snapshotDir(t, bootStorageDir)
		for k, v := range beforeSnap {
			if afterSnap[k] != v {
				t.Errorf("boot storage modified at %s", k)
			}
		}
	})

	// Step 3: partuuid mismatch and secure boot tier
	t.Run("Refusal_Step3_PartUUIDMismatch", func(t *testing.T) {
		rootDir, bootStorageDir, _ := setupTestEnv(t, "both")
		setupKernelFiles(t, rootDir, "test")
		beforeSnap := snapshotDir(t, bootStorageDir)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			CmdlineContent: "root=UUID=111 ro vintner.bootstorage=PARTUUID=00000000-0000-0000-0000-000000000099",
		}
		_, err := Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected refusal on partuuid mismatch")
		}
		afterSnap := snapshotDir(t, bootStorageDir)
		for k, v := range beforeSnap {
			if afterSnap[k] != v {
				t.Errorf("boot storage modified at %s", k)
			}
		}
	})

	// Step 5: dpkg-query failure
	t.Run("Refusal_Step5_NoPackage", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "both")
		setupKernelFiles(t, rootDir, "test")
		beforeSnap := snapshotDir(t, bootStorageDir)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			CmdlineContent: m.Images[0].Cmdline,
			PackageQuery:   mockPackageQuery{fail: true},
		}
		_, err := Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected refusal on package lookup failure")
		}
		afterSnap := snapshotDir(t, bootStorageDir)
		for k, v := range beforeSnap {
			if afterSnap[k] != v {
				t.Errorf("boot storage modified at %s", k)
			}
		}
	})

	// Step 7: grubenv exceeds 512 bytes
	t.Run("Refusal_Step7_GrubenvFull", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "bios")
		setupKernelFiles(t, rootDir, "test")
		beforeSnap := snapshotDir(t, bootStorageDir)

		// Fill grubenv close to 512 bytes
		grubenvPath := filepath.Join(bootStorageDir, "grub", "grubenv")
		env, err := grubenv.Read(grubenvPath)
		if err != nil {
			t.Fatal(err)
		}
		env.Set("FILLER", strings.Repeat("a", 370))
		if err := grubenv.Write(grubenvPath, env); err != nil {
			t.Fatalf("setup full grubenv: %v", err)
		}
		beforeSnap = snapshotDir(t, bootStorageDir)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
			CmdlineContent: m.Images[0].Cmdline,
			PackageQuery:   mockPackageQuery{pkg: "p", ver: "1"},
		}
		_, err = Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected refusal on grubenv overflow")
		}
		afterSnap := snapshotDir(t, bootStorageDir)
		for k, v := range beforeSnap {
			if afterSnap[k] != v {
				t.Errorf("boot storage modified at %s", k)
			}
		}
	})

	// Step 10: staged UKI verification mismatch
	t.Run("Refusal_Step10_VerificationMismatch", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "test")
		beforeSnap := snapshotDir(t, bootStorageDir)

		// Build UKI with wrong cmdline to trigger verification failure
		badBuilder := mockUKIBuilderFunc(func(ctx context.Context, req UKIRequest) error {
			osrelData, _ := os.ReadFile(req.OSReleasePath)
			initrdData, _ := os.ReadFile(req.InitrdPath)
			return writeTestPE(req.OutputPath, osrelData, []byte("wrong-cmdline"), []byte(req.Uname), initrdData)
		})

		opts := Options{
			KernelABI:        "test",
			BootStorageDir:   bootStorageDir,
			RootDir:          rootDir,
			LockFile:         filepath.Join(rootDir, "run", "publish.lock"),
			CmdlineContent:   m.Images[0].Cmdline,
			PackageQuery:     mockPackageQuery{pkg: "p", ver: "1"},
			UKIBuilder:       badBuilder,
			DiskSpaceChecker: mockDiskChecker{free: 100 * 1024 * 1024},
		}
		_, err := Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected refusal on verification mismatch")
		}
		// The STAGED record was saved before staging, so the refusal removes it with
		// another Save: only the manifest copies change, and the generation is spent.
		afterSnap := snapshotDir(t, bootStorageDir)
		for k, v := range afterSnap {
			if strings.HasPrefix(k, "vintner/state/") {
				continue
			}
			if beforeSnap[k] != v {
				t.Errorf("boot storage file added or changed: %s", k)
			}
		}
		for k := range beforeSnap {
			if _, ok := afterSnap[k]; !ok && !strings.HasPrefix(k, "vintner/state/") {
				t.Errorf("boot storage file removed: %s", k)
			}
		}
		got, err := manifest.Load(filepath.Join(bootStorageDir, "vintner", "state"))
		if err != nil {
			t.Fatalf("manifest after refusal: %v", err)
		}
		if len(got.Images) != len(m.Images) || got.Images[0].Generation != m.Images[0].Generation {
			t.Errorf("images after refusal = %+v, want only generation %d", got.Images, m.Images[0].Generation)
		}
		if got.NextGeneration != m.NextGeneration+1 {
			t.Errorf("next_generation = %d, want %d", got.NextGeneration, m.NextGeneration+1)
		}
	})
}

type mockUKIBuilderFunc func(ctx context.Context, req UKIRequest) error

func (f mockUKIBuilderFunc) Build(ctx context.Context, req UKIRequest) error {
	return f(ctx, req)
}

func TestPublishUnchanged(t *testing.T) {
	rootDir, bootStorageDir, m := setupTestEnv(t, "both")
	abi := "6.12-base"
	setupKernelFiles(t, rootDir, abi)

	// First publish for abi
	pkgQuery := mockPackageQuery{pkg: "linux-image-" + abi, ver: "6.12-1"}
	ukiB := mockUKIBuilder{}
	diskCheck := mockDiskChecker{free: 100 * 1024 * 1024}

	var out1 bytes.Buffer
	opts := Options{
		KernelABI:        abi,
		BootStorageDir:   bootStorageDir,
		RootDir:          rootDir,
		LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
		CmdlineContent:   m.Images[0].Cmdline,
		PackageQuery:     pkgQuery,
		UKIBuilder:       ukiB,
		DiskSpaceChecker: diskCheck,
		Stdout:           &out1,
	}

	res1, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("first publish: %v", err)
	}
	if res1.Unchanged {
		t.Fatal("expected new publish on first run")
	}

	// Second publish with same files
	var out2 bytes.Buffer
	opts.Stdout = &out2
	res2, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("second publish: %v", err)
	}
	if !res2.Unchanged {
		t.Errorf("expected unchanged = true, got false")
	}
	if !strings.Contains(out2.String(), "unchanged") {
		t.Errorf("expected output to contain \"unchanged\", got: %s", out2.String())
	}
}

func TestPublishCrashesAndReconcile(t *testing.T) {
	// Crash after step 9: manifest has STAGED, but files not yet in staging/final place.
	t.Run("Crash_After_Step9", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "both")
		setupKernelFiles(t, rootDir, "test")

		opts := Options{
			KernelABI:        "test",
			BootStorageDir:   bootStorageDir,
			RootDir:          rootDir,
			LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
			CmdlineContent:   m.Images[0].Cmdline,
			PackageQuery:     mockPackageQuery{pkg: "p", ver: "1"},
			UKIBuilder:       mockUKIBuilder{},
			DiskSpaceChecker: mockDiskChecker{free: 100 * 1024 * 1024},
			StopAfterStep9:   true,
		}

		res, err := Publish(context.Background(), opts)
		if err != nil {
			t.Fatalf("Publish step 9: %v", err)
		}
		if res.Generation != 2 {
			t.Fatalf("expected gen 2, got %d", res.Generation)
		}

		// Verify manifest has STAGED
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		mf, _ := manifest.Load(stateDir)
		if len(mf.Images) != 2 || mf.Images[1].State != "STAGED" {
			t.Fatalf("expected image 2 to be STAGED: %+v", mf.Images)
		}

		// Run reconcile
		view, err := state.Scan(bootStorageDir)
		if err != nil {
			t.Fatal(err)
		}
		actions, reports := state.Reconcile(mf, view)
		if len(reports) != 0 {
			t.Errorf("unexpected reports: %v", reports)
		}
		_ = actions
		// Reconcile rule 2 removed the STAGED record
		if len(mf.Images) != 1 {
			t.Errorf("reconcile should have removed STAGED image: got %d images", len(mf.Images))
		}
	})

	// Crash after step 10: files in staging, manifest has STAGED.
	t.Run("Crash_After_Step10", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "both")
		setupKernelFiles(t, rootDir, "test")

		opts := Options{
			KernelABI:        "test",
			BootStorageDir:   bootStorageDir,
			RootDir:          rootDir,
			LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
			CmdlineContent:   m.Images[0].Cmdline,
			PackageQuery:     mockPackageQuery{pkg: "p", ver: "1"},
			UKIBuilder:       mockUKIBuilder{},
			DiskSpaceChecker: mockDiskChecker{free: 100 * 1024 * 1024},
			StopAfterStep10:  true,
		}

		res, err := Publish(context.Background(), opts)
		if err != nil {
			t.Fatalf("Publish step 10: %v", err)
		}
		if res.Generation != 2 {
			t.Fatalf("expected gen 2, got %d", res.Generation)
		}

		// Files are in staging
		stagingDir := filepath.Join(bootStorageDir, "vintner", "staging")
		entries, _ := os.ReadDir(stagingDir)
		if len(entries) == 0 {
			t.Fatal("expected files in staging")
		}

		// Run reconcile
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		mf, _ := manifest.Load(stateDir)
		view, err := state.Scan(bootStorageDir)
		if err != nil {
			t.Fatal(err)
		}
		actions, reports := state.Reconcile(mf, view)
		if len(reports) != 0 {
			t.Errorf("unexpected reports: %v", reports)
		}
		// Apply actions (remove staging)
		for _, a := range actions {
			if a.Type == "remove_staging" {
				_ = os.RemoveAll(filepath.Join(stagingDir, a.Name))
			}
		}
		// Reconcile rule 2 removed the STAGED record and requested staging removal
		if len(mf.Images) != 1 {
			t.Errorf("reconcile should have removed STAGED image: got %d images", len(mf.Images))
		}
		entriesAfter, _ := os.ReadDir(stagingDir)
		if len(entriesAfter) != 0 {
			t.Errorf("staging should be cleaned: %d entries remaining", len(entriesAfter))
		}
	})
}

func TestPublishStaleAndGarbageCollection(t *testing.T) {
	rootDir, bootStorageDir, m := setupTestEnv(t, "both")
	m.Retain = 2
	stateDir := filepath.Join(bootStorageDir, "vintner", "state")
	_ = manifest.Save(stateDir, m)

	setupKernelFiles(t, rootDir, "abi-2")
	setupKernelFiles(t, rootDir, "abi-3")

	pkgQuery := mockPackageQuery{pkg: "pkg", ver: "1"}
	ukiB := mockUKIBuilder{}
	diskCheck := mockDiskChecker{free: 100 * 1024 * 1024}

	opts2 := Options{
		KernelABI:        "abi-2",
		BootStorageDir:   bootStorageDir,
		RootDir:          rootDir,
		LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
		CmdlineContent:   m.Images[0].Cmdline,
		PackageQuery:     pkgQuery,
		UKIBuilder:       ukiB,
		DiskSpaceChecker: diskCheck,
	}
	_, err := Publish(context.Background(), opts2)
	if err != nil {
		t.Fatalf("publish abi-2: %v", err)
	}

	// Remove kernel for abi-2 to make it stale
	_ = os.Remove(filepath.Join(rootDir, "boot", "vmlinuz-abi-2"))

	opts3 := Options{
		KernelABI:        "abi-3",
		BootStorageDir:   bootStorageDir,
		RootDir:          rootDir,
		LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
		CmdlineContent:   m.Images[0].Cmdline,
		PackageQuery:     pkgQuery,
		UKIBuilder:       ukiB,
		DiskSpaceChecker: diskCheck,
	}
	_, err = Publish(context.Background(), opts3)
	if err != nil {
		t.Fatalf("publish abi-3: %v", err)
	}

	// Verify manifest
	mf, err := manifest.Load(stateDir)
	if err != nil {
		t.Fatal(err)
	}

	// Check stale flag on gen 2
	for _, img := range mf.Images {
		if img.Kernel.ABI == "abi-2" {
			if !img.Stale {
				t.Errorf("expected image abi-2 to be stale")
			}
		}
	}
}

func TestNotVintnerSystem(t *testing.T) {
	tmpDir := t.TempDir()
	opts := Options{
		KernelABI:      "6.12",
		CmdlineContent: "root=UUID=123 ro quiet",
		BootStorageDir: tmpDir,
	}
	res, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Unchanged {
		t.Errorf("expected unchanged result for non-vintner system")
	}
}

func TestInitrdDoesNotExistYet(t *testing.T) {
	rootDir, bootStorageDir, m := setupTestEnv(t, "both")
	// write only vmlinuz, no initrd
	vmlinuzPath := filepath.Join(rootDir, "boot", "vmlinuz-newabi")
	_ = os.WriteFile(vmlinuzPath, []byte("vmlinuz"), 0644)

	opts := Options{
		KernelABI:      "newabi",
		BootStorageDir: bootStorageDir,
		RootDir:        rootDir,
		CmdlineContent: m.Images[0].Cmdline,
	}
	res, err := Publish(context.Background(), opts)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !res.Unchanged {
		t.Errorf("expected unchanged result when initrd does not exist yet")
	}
}

func TestRefusalsAdditional(t *testing.T) {
	t.Run("SecureBootTierNotNone", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "test")
		m.SecureBootTier = "A"
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		_ = manifest.Save(stateDir, m)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			CmdlineContent: m.Images[0].Cmdline,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
		}
		_, err := Publish(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "unsupported secure boot tier") {
			t.Fatalf("expected unsupported secure boot tier error, got: %v", err)
		}
	})

	t.Run("ManifestInvalid", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "test")
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		_ = os.WriteFile(filepath.Join(stateDir, "manifest-a"), []byte("corrupt"), 0644)
		_ = os.WriteFile(filepath.Join(stateDir, "manifest-b"), []byte("corrupt"), 0644)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			CmdlineContent: m.Images[0].Cmdline,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
		}
		_, err := Publish(context.Background(), opts)
		if err == nil {
			t.Fatal("expected error on corrupt manifest")
		}
	})

	t.Run("CmdlineMissingRoot", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "test")
		m.Images[0].Cmdline = "ro vintner.bootstorage=PARTUUID=" + m.BootStorage.PartUUID
		stateDir := filepath.Join(bootStorageDir, "vintner", "state")
		_ = manifest.Save(stateDir, m)

		opts := Options{
			KernelABI:      "test",
			BootStorageDir: bootStorageDir,
			RootDir:        rootDir,
			CmdlineContent: "root=UUID=" + m.Root.UUID + " ro vintner.bootstorage=PARTUUID=" + m.BootStorage.PartUUID,
			LockFile:       filepath.Join(bootStorageDir, "publish.lock"),
		}
		_, err := Publish(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "cmdline lacks root=") {
			t.Fatalf("expected error on missing root=, got: %v", err)
		}
	})

	t.Run("InsufficientDiskSpace", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "test")

		opts := Options{
			KernelABI:        "test",
			BootStorageDir:   bootStorageDir,
			RootDir:          rootDir,
			CmdlineContent:   m.Images[0].Cmdline,
			LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
			PackageQuery:     mockPackageQuery{pkg: "p", ver: "1"},
			UKIBuilder:       mockUKIBuilder{},
			DiskSpaceChecker: mockDiskChecker{free: 10}, // only 10 bytes free
		}
		_, err := Publish(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "insufficient room on boot storage") {
			t.Fatalf("expected insufficient room error, got: %v", err)
		}
	})

	t.Run("MissingStub", func(t *testing.T) {
		rootDir, bootStorageDir, m := setupTestEnv(t, "uefi")
		setupKernelFiles(t, rootDir, "test")
		_ = os.Remove(filepath.Join(rootDir, "usr", "lib", "systemd", "boot", "efi", "linuxx64.efi.stub"))

		opts := Options{
			KernelABI:        "test",
			BootStorageDir:   bootStorageDir,
			RootDir:          rootDir,
			CmdlineContent:   m.Images[0].Cmdline,
			LockFile:         filepath.Join(bootStorageDir, "publish.lock"),
			PackageQuery:     mockPackageQuery{pkg: "p", ver: "1"},
			UKIBuilder:       mockUKIBuilder{},
			DiskSpaceChecker: mockDiskChecker{free: 100 * 1024 * 1024},
		}
		_, err := Publish(context.Background(), opts)
		if err == nil || !strings.Contains(err.Error(), "stub not found") {
			t.Fatalf("expected stub not found error, got: %v", err)
		}
	})
}

func TestDKMSAndModuleTreeHelpers(t *testing.T) {
	tmpDir := t.TempDir()
	dkmsDir := filepath.Join(tmpDir, "dkms")
	_ = os.MkdirAll(filepath.Join(dkmsDir, "subdir"), 0755)

	_ = os.WriteFile(filepath.Join(dkmsDir, "mod1.ko"), []byte("content1"), 0644)
	_ = os.WriteFile(filepath.Join(dkmsDir, "subdir", "mod2.ko"), []byte("content2"), 0644)

	h1, err := ComputeDKMSSHA256(dkmsDir)
	if err != nil {
		t.Fatalf("ComputeDKMSSHA256: %v", err)
	}
	if h1 == "" {
		t.Errorf("expected non-empty hash")
	}

	// Non-existent directory returns sha256("")
	hEmpty, err := ComputeDKMSSHA256(filepath.Join(tmpDir, "nonexistent"))
	if err != nil {
		t.Fatalf("ComputeDKMSSHA256 empty: %v", err)
	}
	emptySHA := "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	if hEmpty != emptySHA {
		t.Errorf("got %s, want %s", hEmpty, emptySHA)
	}

	// ModuleTreeMatches
	rootDir := filepath.Join(tmpDir, "root")
	setupKernelFiles(t, rootDir, "testabi")
	mt, err := ComputeModuleTree(rootDir, "testabi")
	if err != nil {
		t.Fatalf("ComputeModuleTree: %v", err)
	}
	if !ModuleTreeMatches(rootDir, "testabi", mt) {
		t.Errorf("expected ModuleTreeMatches to return true")
	}

	// Corrupt kernel
	_ = os.WriteFile(filepath.Join(rootDir, "boot", "vmlinuz-testabi"), []byte("modified"), 0644)
	if ModuleTreeMatches(rootDir, "testabi", mt) {
		t.Errorf("expected ModuleTreeMatches to return false after kernel modification")
	}
}

func TestDefaultLockPath(t *testing.T) {
	tmpDir := t.TempDir()
	p := DefaultLockPath(tmpDir)
	if p == "" {
		t.Errorf("expected non-empty lock path")
	}
	p2 := DefaultLockPath("")
	if p2 != "/run/vintner/publish.lock" {
		t.Errorf("got %s, want /run/vintner/publish.lock", p2)
	}
}
