// SPDX-License-Identifier: GPL-2.0-or-later

package publish

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/pe"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/bls"
	"github.com/zcomputerwiz/vintner-boot/src/internal/bootstorage"
	"github.com/zcomputerwiz/vintner-boot/src/internal/grubenv"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/state"
	"github.com/zcomputerwiz/vintner-boot/src/internal/testpoint"
	"github.com/zcomputerwiz/vintner-boot/src/internal/uki"
)

type PackageQuery interface {
	PackageAndVersion(vmlinuzPath string) (pkg string, ver string, err error)
}

type UKIRequest struct {
	LinuxPath     string
	InitrdPath    string
	StubPath      string
	OSReleasePath string
	Cmdline       string
	Uname         string
	OutputPath    string
}

type UKIBuilder interface {
	Build(ctx context.Context, req UKIRequest) error
}

type DiskSpaceChecker interface {
	FreeBytes(path string) (uint64, error)
}

type Clock interface {
	Now() time.Time
}

type RealPackageQuery struct{}

func (RealPackageQuery) PackageAndVersion(vmlinuzPath string) (string, string, error) {
	out, err := exec.Command("dpkg-query", "-S", vmlinuzPath).Output()
	if err != nil {
		return "", "", fmt.Errorf("dpkg-query -S %s: %w", vmlinuzPath, err)
	}

	line := strings.TrimSpace(string(out))
	colonIdx := strings.Index(line, ":")
	if colonIdx == -1 {
		return "", "", fmt.Errorf("unexpected dpkg-query -S output: %s", line)
	}
	pkg := strings.TrimSpace(line[:colonIdx])
	if archIdx := strings.Index(pkg, ":"); archIdx != -1 {
		pkg = pkg[:archIdx]
	}

	verOut, err := exec.Command("dpkg-query", "-W", "-f=${Version}", pkg).Output()
	if err != nil {
		return "", "", fmt.Errorf("dpkg-query -W -f=${Version} %s: %w", pkg, err)
	}
	ver := strings.TrimSpace(string(verOut))
	if ver == "" {
		return "", "", fmt.Errorf("empty version for package %s", pkg)
	}
	return pkg, ver, nil
}

type RealUKIBuilder struct{}

func (RealUKIBuilder) Build(ctx context.Context, req UKIRequest) error {
	ukifyPath := ""
	for _, p := range []string{"/usr/bin/ukify", "/usr/lib/systemd/ukify"} {
		if _, err := os.Stat(p); err == nil {
			ukifyPath = p
			break
		}
	}
	if ukifyPath == "" {
		var err error
		ukifyPath, err = exec.LookPath("ukify")
		if err != nil {
			return errors.New("ukify not found")
		}
	}

	if _, err := os.Stat(req.StubPath); err != nil {
		return fmt.Errorf("stub not found: %s", req.StubPath)
	}

	cmd := exec.CommandContext(ctx, ukifyPath, "build",
		"--linux="+req.LinuxPath,
		"--initrd="+req.InitrdPath,
		"--stub="+req.StubPath,
		"--os-release=@"+req.OSReleasePath,
		"--cmdline="+req.Cmdline,
		"--uname="+req.Uname,
		"--output="+req.OutputPath,
	)
	var errBuf bytes.Buffer
	cmd.Stderr = &errBuf
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("ukify build failed: %w: %s", err, errBuf.String())
	}
	return nil
}

type RealDiskSpaceChecker struct{}

func (RealDiskSpaceChecker) FreeBytes(path string) (uint64, error) {
	return statfsFreeBytes(path)
}

type RealClock struct{}

func (RealClock) Now() time.Time {
	return time.Now().UTC()
}

type Options struct {
	KernelABI        string
	BootStorageDir   string
	StateDir         string
	RootDir          string
	LockFile         string
	CmdlineFile      string
	CmdlineContent   string
	PackageQuery     PackageQuery
	UKIBuilder       UKIBuilder
	DiskSpaceChecker DiskSpaceChecker
	Clock            Clock
	Stdout           io.Writer
	Stderr           io.Writer

	// Test hooks to simulate crashes
	StopAfterStep9  bool
	StopAfterStep10 bool
}

type Result struct {
	Unchanged  bool
	Generation int
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Sync()
}

func readSection(sec *pe.Section) ([]byte, error) {
	data, err := sec.Data()
	if err != nil {
		return nil, err
	}
	// SizeOfRawData is file-aligned; VirtualSize is the content's length.
	if sec.VirtualSize > 0 && sec.VirtualSize < uint32(len(data)) {
		data = data[:sec.VirtualSize]
	}
	return data, nil
}

func ukiSectionSHA256(ukiPath, sectionName string) (string, error) {
	f, err := pe.Open(ukiPath)
	if err != nil {
		return "", err
	}
	defer f.Close()

	for _, sec := range f.Sections {
		if sec.Name == sectionName {
			data, err := readSection(sec)
			if err != nil {
				return "", err
			}
			h := sha256.Sum256(data)
			return hex.EncodeToString(h[:]), nil
		}
	}
	return "", fmt.Errorf("section %s not found in %s", sectionName, ukiPath)
}

func ukiInitrdSHA256(ukiPath string) (string, error) {
	return ukiSectionSHA256(ukiPath, ".initrd")
}

func generateOSRelease(origPath, outPath string, gen int, abi string) error {
	data, err := os.ReadFile(origPath)
	if err != nil {
		return err
	}

	var lines []string
	origPretty := ""
	for _, l := range strings.Split(string(data), "\n") {
		trimmed := strings.TrimSpace(l)
		if strings.HasPrefix(trimmed, "PRETTY_NAME=") {
			val := strings.TrimPrefix(trimmed, "PRETTY_NAME=")
			if unquoted, err := strconv.Unquote(val); err == nil {
				origPretty = unquoted
			} else {
				origPretty = strings.Trim(val, `"'`)
			}
			continue
		}
		if strings.HasPrefix(trimmed, "IMAGE_ID=") || strings.HasPrefix(trimmed, "IMAGE_VERSION=") {
			continue
		}
		if l != "" {
			lines = append(lines, l)
		}
	}

	newPretty := origPretty
	if newPretty != "" {
		newPretty += ", " + abi
	} else {
		newPretty = abi
	}

	lines = append(lines,
		fmt.Sprintf("PRETTY_NAME=%q", newPretty),
		"IMAGE_ID=vintner-os",
		fmt.Sprintf("IMAGE_VERSION=%d", gen),
	)

	return os.WriteFile(outPath, []byte(strings.Join(lines, "\n")+"\n"), 0644)
}

func Publish(ctx context.Context, opts Options) (*Result, error) {
	abi := opts.KernelABI
	if abi == "" {
		return nil, errors.New("kernel ABI (--kernel) is required")
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = os.Stderr
	}

	pkgQuery := opts.PackageQuery
	if pkgQuery == nil {
		pkgQuery = RealPackageQuery{}
	}
	ukiBuilder := opts.UKIBuilder
	if ukiBuilder == nil {
		ukiBuilder = RealUKIBuilder{}
	}
	diskChecker := opts.DiskSpaceChecker
	if diskChecker == nil {
		diskChecker = RealDiskSpaceChecker{}
	}
	clk := opts.Clock
	if clk == nil {
		clk = RealClock{}
	}

	rootDir := opts.RootDir
	if rootDir == "" {
		rootDir = "/"
	}

	// 1. Not a vintner system, or nothing to publish yet.
	var bootCmdline string
	if opts.CmdlineContent != "" {
		bootCmdline = opts.CmdlineContent
	} else {
		cmdlinePath := opts.CmdlineFile
		if cmdlinePath == "" {
			cmdlinePath = "/proc/cmdline"
		}
		data, err := os.ReadFile(cmdlinePath)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Fprintln(stdout, "vintner publish: not a vintner system (cannot read cmdline)")
				return &Result{Unchanged: true}, nil
			}
			return nil, fmt.Errorf("read cmdline: %w", err)
		}
		bootCmdline = string(data)
	}

	if !strings.Contains(bootCmdline, "vintner.bootstorage=") {
		fmt.Fprintln(stdout, "vintner publish: not a vintner system (no vintner.bootstorage=)")
		return &Result{Unchanged: true}, nil
	}

	initrdPath := filepath.Join(rootDir, "boot", "initrd.img-"+abi)
	if _, err := os.Stat(initrdPath); err != nil {
		if os.IsNotExist(err) {
			fmt.Fprintln(stdout, "vintner publish: initramfs does not exist yet for "+abi)
			return &Result{Unchanged: true}, nil
		}
		return nil, fmt.Errorf("stat initramfs: %w", err)
	}

	vmlinuzPath := filepath.Join(rootDir, "boot", "vmlinuz-"+abi)
	if _, err := os.Stat(vmlinuzPath); err != nil {
		return nil, fmt.Errorf("vmlinuz does not exist: %s", vmlinuzPath)
	}

	// 2. Lock.
	lockPath := opts.LockFile
	if lockPath == "" {
		lockPath = DefaultLockPath(opts.BootStorageDir)
	}
	lock, err := AcquireLock(lockPath)
	if err != nil {
		return nil, fmt.Errorf("acquire lock %s: %w", lockPath, err)
	}
	defer lock.Close()

	// 3. Boot storage and manifest.
	bootStorageDir := opts.BootStorageDir
	var unmount func() error
	if bootStorageDir == "" {
		dev, err := bootstorage.Device(bootCmdline)
		if err != nil {
			return nil, fmt.Errorf("resolve boot storage device: %w", err)
		}
		unmount, err = bootstorage.Mount(dev, true)
		if err != nil {
			return nil, fmt.Errorf("mount boot storage: %w", err)
		}
		bootStorageDir = bootstorage.MountPoint
		defer func() {
			if unmount != nil {
				_ = unmount()
			}
		}()
	}

	stateDir := opts.StateDir
	if stateDir == "" {
		stateDir = filepath.Join(bootStorageDir, "vintner", "state")
	}

	m, err := manifest.Load(stateDir)
	if err != nil {
		return nil, fmt.Errorf("failed to load manifest: %w", err)
	}

	// Verify boot_storage.partuuid matches vintner.bootstorage=
	cmdFields := strings.Fields(bootCmdline)
	var bsPartUUID string
	for _, f := range cmdFields {
		if strings.HasPrefix(f, "vintner.bootstorage=PARTUUID=") {
			bsPartUUID = strings.TrimPrefix(f, "vintner.bootstorage=PARTUUID=")
			break
		}
	}
	if bsPartUUID == "" || !strings.EqualFold(m.BootStorage.PartUUID, bsPartUUID) {
		return nil, fmt.Errorf("boot storage partuuid mismatch: manifest=%s, cmdline=%s", m.BootStorage.PartUUID, bsPartUUID)
	}

	if m.SecureBootTier != "none" {
		return nil, fmt.Errorf("unsupported secure boot tier %q (only \"none\" supported)", m.SecureBootTier)
	}

	// 4. Reconcile.
	v, err := state.Scan(bootStorageDir)
	if err != nil {
		return nil, fmt.Errorf("scan boot storage: %w", err)
	}

	actions, reports := state.Reconcile(m, v)
	for _, r := range reports {
		fmt.Fprintln(stderr, "REPORT:", r)
	}
	for _, a := range actions {
		fmt.Fprintf(stdout, "ACTION: %s %s\n", a.Type, a.Name)
		if a.Type == "remove_staging" {
			p := filepath.Join(bootStorageDir, "vintner", "staging", a.Name)
			_ = os.RemoveAll(p)
		}
	}
	if len(actions) > 0 || len(reports) > 0 {
		if err := m.Validate(); err != nil {
			return nil, fmt.Errorf("manifest after reconcile: %w", err)
		}
		if err := manifest.Save(stateDir, m); err != nil {
			return nil, fmt.Errorf("save manifest after reconcile: %w", err)
		}
	}

	// 5. Inputs.
	// Command line from newest GOOD image, or newest image if none is GOOD
	var baseCmdline string
	newestGoodGen := -1
	newestAnyGen := -1

	for _, img := range m.Images {
		if img.Generation > newestAnyGen {
			newestAnyGen = img.Generation
			if newestGoodGen == -1 {
				baseCmdline = img.Cmdline
			}
		}
		if img.State == "GOOD" && img.Generation > newestGoodGen {
			newestGoodGen = img.Generation
			baseCmdline = img.Cmdline
		}
	}
	if baseCmdline == "" {
		return nil, errors.New("no image command line found in manifest")
	}

	baseFields := strings.Fields(baseCmdline)
	hasRoot := false
	bsCount := 0
	for _, f := range baseFields {
		if f == "root=UUID="+m.Root.UUID || f == "root=PARTUUID="+m.Root.PartUUID ||
			strings.EqualFold(f, "root=UUID="+m.Root.UUID) || strings.EqualFold(f, "root=PARTUUID="+m.Root.PartUUID) {
			hasRoot = true
		}
		if strings.HasPrefix(f, "vintner.bootstorage=") {
			bsCount++
			if f != "vintner.bootstorage=PARTUUID="+m.BootStorage.PartUUID &&
				!strings.EqualFold(f, "vintner.bootstorage=PARTUUID="+m.BootStorage.PartUUID) {
				return nil, fmt.Errorf("invalid vintner.bootstorage in cmdline: %s", f)
			}
		}
	}
	if !hasRoot {
		return nil, fmt.Errorf("cmdline lacks root=UUID=%s or root=PARTUUID=%s", m.Root.UUID, m.Root.PartUUID)
	}
	if bsCount != 1 {
		return nil, fmt.Errorf("cmdline must contain exactly one vintner.bootstorage=PARTUUID=%s (found %d)", m.BootStorage.PartUUID, bsCount)
	}

	pkgName, pkgVer, err := pkgQuery.PackageAndVersion(vmlinuzPath)
	if err != nil {
		return nil, fmt.Errorf("package lookup for %s: %w", vmlinuzPath, err)
	}

	modTree, err := ComputeModuleTree(rootDir, abi)
	if err != nil {
		return nil, fmt.Errorf("compute module tree: %w", err)
	}

	// 6. Unchanged input.
	var newestSameABI *manifest.Image
	for i := range m.Images {
		img := &m.Images[i]
		if img.Kernel.ABI == abi {
			if newestSameABI == nil || img.Generation > newestSameABI.Generation {
				newestSameABI = img
			}
		}
	}

	if newestSameABI != nil && newestSameABI.State != "FAILED" {
		currentKernelSHA, err := fileSHA256(vmlinuzPath)
		var expectedKernelSHA string
		if newestSameABI.ModuleTree != nil {
			expectedKernelSHA = newestSameABI.ModuleTree.KernelSHA256
		} else if newestSameABI.BIOS != nil {
			expectedKernelSHA = newestSameABI.BIOS.Files.Vmlinuz
		} else {
			efiDir := filepath.Join(bootStorageDir, "EFI", "Linux")
			stem := fmt.Sprintf("vintner-os-%d", newestSameABI.Generation)
			if ukiFile, err := bls.Find(efiDir, stem); err == nil {
				if peSHA, err := ukiSectionSHA256(filepath.Join(efiDir, ukiFile), ".linux"); err == nil {
					expectedKernelSHA = peSHA
				}
			}
		}

		if err == nil && expectedKernelSHA != "" && expectedKernelSHA == currentKernelSHA && newestSameABI.Cmdline == baseCmdline {
			currentInitrdSHA, err := fileSHA256(initrdPath)
			if err == nil {
				initrdMatch := false
				if newestSameABI.BIOS != nil {
					initrdMatch = (newestSameABI.BIOS.Files.InitrdImg == currentInitrdSHA)
				} else {
					efiDir := filepath.Join(bootStorageDir, "EFI", "Linux")
					stem := fmt.Sprintf("vintner-os-%d", newestSameABI.Generation)
					if ukiFile, err := bls.Find(efiDir, stem); err == nil {
						if peSHA, err := ukiInitrdSHA256(filepath.Join(efiDir, ukiFile)); err == nil {
							initrdMatch = (peSHA == currentInitrdSHA)
						}
					}
				}
				if initrdMatch {
					fmt.Fprintln(stdout, "unchanged")
					// Jump to Step 12
					return finishPublish(stdout, stderr, m, stateDir, bootStorageDir, rootDir, newestSameABI.Generation, true)
				}
			}
		}
	}

	// 7. Check that grubenv has room.
	g := m.NextGeneration
	isBIOS := (m.Firmware == "bios" || m.Firmware == "both")
	isUEFI := (m.Firmware == "uefi" || m.Firmware == "both")
	grubenvPath := filepath.Join(bootStorageDir, "grub", "grubenv")

	if isBIOS {
		env, err := grubenv.Read(grubenvPath)
		if err != nil {
			return nil, fmt.Errorf("read grubenv: %w", err)
		}
		testEnv := env.Clone()
		testEnv.Set(fmt.Sprintf("VINTNER_T_%d", g), strings.Repeat("x", m.Tries))
		order, _ := testEnv.Get("VINTNER_ORDER")
		if order == "" {
			order = strconv.Itoa(g)
		} else {
			order = strconv.Itoa(g) + " " + order
		}
		testEnv.Set("VINTNER_ORDER", order)
		if testEnv.EncodedLen() > 512 {
			return nil, errors.New("grubenv would exceed 512 bytes in first sector")
		}
	}

	// 8. Build the image files under /var/tmp/.
	varTmpDir := filepath.Join(rootDir, "var", "tmp")
	_ = os.MkdirAll(varTmpDir, 0755)
	buildDir, err := os.MkdirTemp(varTmpDir, "vintner-publish-*")
	if err != nil {
		buildDir, err = os.MkdirTemp("", "vintner-publish-*")
		if err != nil {
			return nil, fmt.Errorf("create build temp dir: %w", err)
		}
	}
	defer os.RemoveAll(buildDir)

	// Snapshot the inputs: /boot can change under a running publish (update-initramfs
	// replaces the initramfs without our lock), and every format must carry the same bytes.
	srcVmlinuz := filepath.Join(buildDir, "vmlinuz")
	srcInitrd := filepath.Join(buildDir, "initrd.img")
	if err := copyFile(vmlinuzPath, srcVmlinuz); err != nil {
		return nil, fmt.Errorf("copy kernel to build dir: %w", err)
	}
	if err := copyFile(initrdPath, srcInitrd); err != nil {
		return nil, fmt.Errorf("copy initramfs to build dir: %w", err)
	}

	var ukiBuiltPath string
	var ukiSHA string
	var ukiSize int

	if isUEFI {
		osReleaseOrig := filepath.Join(rootDir, "etc", "os-release")
		osReleaseTmp := filepath.Join(buildDir, "os-release")
		if err := generateOSRelease(osReleaseOrig, osReleaseTmp, g, abi); err != nil {
			return nil, fmt.Errorf("generate os-release: %w", err)
		}

		stubPath := filepath.Join(rootDir, "usr", "lib", "systemd", "boot", "efi", "linuxx64.efi.stub")
		if _, err := os.Stat(stubPath); err != nil {
			return nil, fmt.Errorf("stub not found: %s", stubPath)
		}

		ukiBuiltPath = filepath.Join(buildDir, fmt.Sprintf("vintner-os-%d.efi", g))
		err = ukiBuilder.Build(ctx, UKIRequest{
			LinuxPath:     srcVmlinuz,
			InitrdPath:    srcInitrd,
			StubPath:      stubPath,
			OSReleasePath: osReleaseTmp,
			Cmdline:       baseCmdline,
			Uname:         abi,
			OutputPath:    ukiBuiltPath,
		})
		if err != nil {
			return nil, fmt.Errorf("build UKI: %w", err)
		}

		fi, err := os.Stat(ukiBuiltPath)
		if err != nil {
			return nil, fmt.Errorf("stat built UKI: %w", err)
		}
		ukiSize = int(fi.Size())
		ukiSHA, err = fileSHA256(ukiBuiltPath)
		if err != nil {
			return nil, fmt.Errorf("hash built UKI: %w", err)
		}
	}

	var slotCfgBuiltPath string
	var vmlinuzSHA, initrdSHA, slotCfgSHA string
	var biosSize int64

	if isBIOS {
		slotCfgBuiltPath = filepath.Join(buildDir, "slot.cfg")
		slotCfgContent := fmt.Sprintf("set vintner_cmdline=%q\n", baseCmdline)
		if err := os.WriteFile(slotCfgBuiltPath, []byte(slotCfgContent), 0644); err != nil {
			return nil, fmt.Errorf("write slot.cfg: %w", err)
		}

		vmlinuzSHA, err = fileSHA256(srcVmlinuz)
		if err != nil {
			return nil, fmt.Errorf("hash vmlinuz: %w", err)
		}
		initrdSHA, err = fileSHA256(srcInitrd)
		if err != nil {
			return nil, fmt.Errorf("hash initrd: %w", err)
		}
		slotCfgSHA, err = fileSHA256(slotCfgBuiltPath)
		if err != nil {
			return nil, fmt.Errorf("hash slot.cfg: %w", err)
		}

		fiV, _ := os.Stat(srcVmlinuz)
		fiI, _ := os.Stat(srcInitrd)
		fiS, _ := os.Stat(slotCfgBuiltPath)
		if fiV != nil {
			biosSize += fiV.Size()
		}
		if fiI != nil {
			biosSize += fiI.Size()
		}
		if fiS != nil {
			biosSize += fiS.Size()
		}
	}

	// Check room on boot storage
	var totalRequired uint64
	if isUEFI {
		totalRequired += uint64(ukiSize)
	}
	if isBIOS {
		totalRequired += uint64(biosSize)
	}
	freeBytes, err := diskChecker.FreeBytes(bootStorageDir)
	if err == nil && totalRequired > freeBytes {
		return nil, fmt.Errorf("insufficient room on boot storage: required %d, free %d", totalRequired, freeBytes)
	}

	// 9. Record STAGED.
	nowRFC := clk.Now().Format(time.RFC3339)
	newImg := manifest.Image{
		Generation: g,
		State:      "STAGED",
		Stale:      false,
		Kernel: manifest.Kernel{
			ABI:     abi,
			Package: &pkgName,
			Version: &pkgVer,
		},
		ModuleTree: modTree,
		Cmdline:    baseCmdline,
		Created:    nowRFC,
		FirstGood:  nil,
		LastGood:   nil,
	}
	if isUEFI {
		newImg.UEFI = &manifest.UEFIFile{
			Entry:  fmt.Sprintf("vintner-os-%d", g),
			SHA256: ukiSHA,
			Size:   ukiSize,
		}
	}
	if isBIOS {
		newImg.BIOS = &manifest.BIOSFiles{
			Dir: fmt.Sprintf("/vintner/bios/os-%d", g),
			Files: manifest.BIOSFilesMap{
				Vmlinuz:   vmlinuzSHA,
				InitrdImg: initrdSHA,
				SlotCfg:   slotCfgSHA,
			},
		}
	}

	m.Images = append(m.Images, newImg)
	m.NextGeneration = g + 1

	if err := m.Validate(); err != nil {
		return nil, fmt.Errorf("manifest validate: %w", err)
	}
	if err := manifest.Save(stateDir, m); err != nil {
		return nil, fmt.Errorf("manifest save staged: %w", err)
	}
	testpoint.Hit("staged-recorded")
	fmt.Fprintf(stdout, "vintner publish: staged generation %d\n", g)

	if opts.StopAfterStep9 {
		return &Result{Generation: g}, nil
	}

	// 10. Stage and verify.
	stagingDir := filepath.Join(bootStorageDir, "vintner", "staging")
	if err := os.MkdirAll(stagingDir, 0755); err != nil {
		return nil, fmt.Errorf("create staging dir: %w", err)
	}

	var stagedUKI string
	var stagedSlotDir string

	cleanupStagingAndRefuse := func(reason error) error {
		if stagedUKI != "" {
			_ = os.Remove(stagedUKI)
		}
		if stagedSlotDir != "" {
			_ = os.RemoveAll(stagedSlotDir)
		}
		_ = syncDir(stagingDir)

		// Remove the STAGED record with an ordinary Save. next_generation stays
		// advanced, so generation g is never reused.
		for i := range m.Images {
			if m.Images[i].Generation == g {
				m.Images = append(m.Images[:i], m.Images[i+1:]...)
				break
			}
		}
		if err := manifest.Save(stateDir, m); err != nil {
			return fmt.Errorf("staging verification failed: %v; removing the record: %w", reason, err)
		}
		return fmt.Errorf("staging verification failed: %w", reason)
	}

	if isUEFI {
		stagedUKI = filepath.Join(stagingDir, fmt.Sprintf("vintner-os-%d.efi", g))
		if err := copyFile(ukiBuiltPath, stagedUKI); err != nil {
			return nil, cleanupStagingAndRefuse(err)
		}
	}

	if isBIOS {
		stagedSlotDir = filepath.Join(stagingDir, fmt.Sprintf("os-%d", g))
		if err := os.MkdirAll(stagedSlotDir, 0755); err != nil {
			return nil, cleanupStagingAndRefuse(err)
		}
		if err := copyFile(srcVmlinuz, filepath.Join(stagedSlotDir, "vmlinuz")); err != nil {
			return nil, cleanupStagingAndRefuse(err)
		}
		if err := copyFile(srcInitrd, filepath.Join(stagedSlotDir, "initrd.img")); err != nil {
			return nil, cleanupStagingAndRefuse(err)
		}
		if err := copyFile(slotCfgBuiltPath, filepath.Join(stagedSlotDir, "slot.cfg")); err != nil {
			return nil, cleanupStagingAndRefuse(err)
		}
		if err := syncDir(stagedSlotDir); err != nil {
			return nil, cleanupStagingAndRefuse(err)
		}
	}

	testpoint.Hit("staging-written")

	if err := syncDir(stagingDir); err != nil {
		return nil, cleanupStagingAndRefuse(err)
	}

	// Read back and compare SHA-256
	if isUEFI {
		readSHA, err := fileSHA256(stagedUKI)
		if err != nil || readSHA != ukiSHA {
			return nil, cleanupStagingAndRefuse(fmt.Errorf("readback UKI sha256 mismatch: got %s, want %s", readSHA, ukiSHA))
		}
		uInfo, err := uki.Read(stagedUKI)
		if err != nil {
			return nil, cleanupStagingAndRefuse(fmt.Errorf("uki.Read failed: %w", err))
		}
		if uInfo.OSRelease["IMAGE_ID"] != "vintner-os" {
			return nil, cleanupStagingAndRefuse(fmt.Errorf("IMAGE_ID mismatch: got %q, want %q", uInfo.OSRelease["IMAGE_ID"], "vintner-os"))
		}
		if uInfo.OSRelease["IMAGE_VERSION"] != strconv.Itoa(g) {
			return nil, cleanupStagingAndRefuse(fmt.Errorf("IMAGE_VERSION mismatch: got %q, want %q", uInfo.OSRelease["IMAGE_VERSION"], strconv.Itoa(g)))
		}
		if string(uInfo.Uname) != abi {
			return nil, cleanupStagingAndRefuse(fmt.Errorf(".uname mismatch: got %q, want %q", string(uInfo.Uname), abi))
		}
		if string(uInfo.Cmdline) != baseCmdline {
			return nil, cleanupStagingAndRefuse(fmt.Errorf(".cmdline mismatch: got %q, want %q", string(uInfo.Cmdline), baseCmdline))
		}
	}

	if isBIOS {
		vSHA, _ := fileSHA256(filepath.Join(stagedSlotDir, "vmlinuz"))
		iSHA, _ := fileSHA256(filepath.Join(stagedSlotDir, "initrd.img"))
		sSHA, _ := fileSHA256(filepath.Join(stagedSlotDir, "slot.cfg"))
		if vSHA != vmlinuzSHA || iSHA != initrdSHA || sSHA != slotCfgSHA {
			return nil, cleanupStagingAndRefuse(errors.New("readback BIOS file hashes mismatch"))
		}
	}

	testpoint.Hit("staging-verified")

	if opts.StopAfterStep10 {
		return &Result{Generation: g}, nil
	}

	// 11. Publish.
	if isUEFI {
		efiDir := filepath.Join(bootStorageDir, "EFI", "Linux")
		if err := os.MkdirAll(efiDir, 0755); err != nil {
			return nil, fmt.Errorf("create EFI/Linux dir: %w", err)
		}
		finalUKI := filepath.Join(efiDir, fmt.Sprintf("vintner-os-%d+%d-0.efi", g, m.Tries))
		if err := os.Rename(stagedUKI, finalUKI); err != nil {
			return nil, fmt.Errorf("publish rename UKI: %w", err)
		}
		_ = syncDir(efiDir)
		_ = syncDir(stagingDir)
	}

	if isBIOS {
		biosDir := filepath.Join(bootStorageDir, "vintner", "bios")
		if err := os.MkdirAll(biosDir, 0755); err != nil {
			return nil, fmt.Errorf("create vintner/bios dir: %w", err)
		}
		finalSlotDir := filepath.Join(biosDir, fmt.Sprintf("os-%d", g))
		if err := os.Rename(stagedSlotDir, finalSlotDir); err != nil {
			return nil, fmt.Errorf("publish rename BIOS slot: %w", err)
		}
		_ = syncDir(stagingDir)
		_ = syncDir(biosDir)
	}

	testpoint.Hit("renamed")

	if isBIOS {
		env, err := grubenv.Read(grubenvPath)
		if err != nil {
			return nil, fmt.Errorf("read grubenv for publish: %w", err)
		}
		env.Set(fmt.Sprintf("VINTNER_T_%d", g), strings.Repeat("x", m.Tries))
		order, _ := env.Get("VINTNER_ORDER")
		if order == "" {
			order = strconv.Itoa(g)
		} else {
			order = strconv.Itoa(g) + " " + order
		}
		env.Set("VINTNER_ORDER", order)
		if err := grubenv.Write(grubenvPath, env); err != nil {
			return nil, fmt.Errorf("write grubenv publish: %w", err)
		}
		testpoint.Hit("grubenv-written")
	}

	for i := range m.Images {
		if m.Images[i].Generation == g {
			m.Images[i].State = "TRYING"
			break
		}
	}
	if err := manifest.Save(stateDir, m); err != nil {
		return nil, fmt.Errorf("save manifest TRYING: %w", err)
	}
	testpoint.Hit("trying-recorded")
	fmt.Fprintf(stdout, "vintner publish: published generation %d\n", g)

	// 12 to 14.
	return finishPublish(stdout, stderr, m, stateDir, bootStorageDir, rootDir, g, false)
}

func finishPublish(stdout, stderr io.Writer, m *manifest.Manifest, stateDir, bootStorageDir, rootDir string, currentGen int, unchanged bool) (*Result, error) {
	// 12. Stale flags.
	flagsChanged := false
	for i := range m.Images {
		img := &m.Images[i]
		if img.ModuleTree != nil {
			matches := ModuleTreeMatches(rootDir, img.Kernel.ABI, img.ModuleTree)
			newStale := !matches
			if img.Stale != newStale {
				img.Stale = newStale
				flagsChanged = true
			}
		}
	}
	if flagsChanged {
		if err := manifest.Save(stateDir, m); err != nil {
			return nil, fmt.Errorf("save manifest stale flags: %w", err)
		}
		fmt.Fprintln(stdout, "vintner publish: updated stale flags")
	}

	// 13. Garbage collection.
	keep, remove, overBudget := state.Keep(m)
	if overBudget {
		fmt.Fprintln(stderr, "vintner publish: image budget exceeded")
	}

	isBIOS := (m.Firmware == "bios" || m.Firmware == "both")
	isUEFI := (m.Firmware == "uefi" || m.Firmware == "both")
	grubenvPath := filepath.Join(bootStorageDir, "grub", "grubenv")

	for _, remGen := range remove {
		// 1. remove its record (Save)
		for idx, im := range m.Images {
			if im.Generation == remGen {
				m.Images = append(m.Images[:idx], m.Images[idx+1:]...)
				break
			}
		}
		if err := manifest.Save(stateDir, m); err != nil {
			return nil, fmt.Errorf("save manifest gc: %w", err)
		}
		testpoint.Hit("gc-record-removed")

		// 2. on BIOS, remove from VINTNER_ORDER and unset counter in one grubenv write
		if isBIOS {
			env, err := grubenv.Read(grubenvPath)
			if err != nil {
				return nil, fmt.Errorf("gc generation %d: read grubenv: %w", remGen, err)
			}
			order, _ := env.Get("VINTNER_ORDER")
			var newFields []string
			remStr := strconv.Itoa(remGen)
			for _, f := range strings.Fields(order) {
				if f != remStr {
					newFields = append(newFields, f)
				}
			}
			env.Set("VINTNER_ORDER", strings.Join(newFields, " "))
			env.Unset(fmt.Sprintf("VINTNER_T_%d", remGen))
			if err := grubenv.Write(grubenvPath, env); err != nil {
				return nil, fmt.Errorf("gc generation %d: write grubenv: %w", remGen, err)
			}
			testpoint.Hit("gc-grubenv-written")
		}

		// 3. remove its BIOS directory and its UKI (found by stem with bls.Find)
		if isBIOS {
			biosSlotDir := filepath.Join(bootStorageDir, "vintner", "bios", fmt.Sprintf("os-%d", remGen))
			_ = os.RemoveAll(biosSlotDir)
		}
		if isUEFI {
			efiDir := filepath.Join(bootStorageDir, "EFI", "Linux")
			stem := fmt.Sprintf("vintner-os-%d", remGen)
			if ukiFile, err := bls.Find(efiDir, stem); err == nil {
				_ = os.Remove(filepath.Join(efiDir, ukiFile))
			}
		}
		fmt.Fprintf(stdout, "vintner publish: removed generation %d\n", remGen)
	}

	// 14. Protect kernel packages of retained images.
	var retainedPackages []string
	for _, kGen := range keep {
		for _, img := range m.Images {
			if img.Generation == kGen && img.Kernel.Package != nil && *img.Kernel.Package != "" {
				retainedPackages = append(retainedPackages, *img.Kernel.Package)
			}
		}
	}
	if err := UpdateRetainedKernels(rootDir, retainedPackages); err != nil {
		return nil, fmt.Errorf("update retained kernels: %w", err)
	}
	fmt.Fprintln(stdout, "vintner publish: updated apt retained kernels")

	return &Result{
		Unchanged:  unchanged,
		Generation: currentGen,
	}, nil
}
