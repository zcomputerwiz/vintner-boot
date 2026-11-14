// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

// fillSecureBoot sets the defaults of the Secure Boot options.
func (o *Options) fillSecureBoot() {
	if o.SecureBootTier == "" {
		o.SecureBootTier = "none"
	}
	if o.SecureBootDir == "" {
		o.SecureBootDir = filepath.Join(o.TargetDir, "etc", "vintner", "secureboot")
	}
	if o.Signer == nil {
		o.Signer = secureboot.SbsignSigner{}
	}
	if o.Verifier == nil {
		o.Verifier = secureboot.SbverifyVerifier{}
	}
}

func (d *deployer) tier() string {
	if manifest.SignedTier(d.SecureBootTier) {
		return d.SecureBootTier
	}
	return "none"
}

// signed reports whether this installation signs with the machine key (tiers A and B).
func (d *deployer) signed() bool { return d.tier() != "none" }

// sbPath is a file of the machine key set; it is empty in tier none, so that
// the UKI builder signs nothing.
func (d *deployer) sbPath(name string) string {
	if !d.signed() {
		return ""
	}
	return filepath.Join(d.SecureBootDir, name)
}

// espCert stands for the machine certificate where a map of ESP files names the
// payload file a file must equal: tier B puts the certificate on the ESP for
// MokManager's "Enroll key from disk" (docs/secure-boot.md §8.4).
const espCert = "secureboot/cert.der"

// espFile is a file vintner writes on the ESP and the payload file it holds.
type espFile struct{ rel, payload string }

// loaderFiles are vintner's own loader files in \EFI\vintner. In tier B the
// boot entry starts shim, which starts systemd-boot under its built-in name
// grubx64.efi and MokManager as mmx64.efi.
func (d *deployer) loaderFiles() []espFile {
	if d.tier() == "B" {
		return []espFile{
			{"EFI/vintner/shimx64.efi", payload.Shim},
			{"EFI/vintner/grubx64.efi", payload.SystemdBoot},
			{"EFI/vintner/mmx64.efi", payload.MokManager},
			{"EFI/vintner/machine-cert.cer", espCert},
		}
	}
	return []espFile{{"EFI/vintner/systemd-bootx64.efi", payload.SystemdBoot}}
}

// removableFiles are what cutoverESP writes in the removable path.
func (d *deployer) removableFiles() []espFile {
	if d.tier() == "B" {
		return []espFile{
			{"EFI/BOOT/BOOTX64.EFI", payload.Shim},
			{"EFI/BOOT/grubx64.efi", payload.SystemdBoot},
			{"EFI/BOOT/mmx64.efi", payload.MokManager},
		}
	}
	return []espFile{{"EFI/BOOT/BOOTX64.EFI", payload.SystemdBoot}}
}

// signedLoaderRel is where the machine-signed systemd-boot lies in \EFI\vintner.
func (d *deployer) signedLoaderRel() string {
	if d.tier() == "B" {
		return "EFI/vintner/grubx64.efi"
	}
	return "EFI/vintner/systemd-bootx64.efi"
}

// newInstallationID returns a random version 4 UUID.
func (d *deployer) newInstallationID() (string, error) {
	id := make([]byte, 16)
	if _, err := io.ReadFull(d.Rand, id); err != nil {
		return "", err
	}
	id[6] = id[6]&0x0f | 0x40
	id[8] = id[8]&0x3f | 0x80
	hx := hex.EncodeToString(id)
	return hx[0:8] + "-" + hx[8:12] + "-" + hx[12:16] + "-" + hx[16:20] + "-" + hx[20:], nil
}

// checkSecureBoot refuses what the tier cannot do, before anything is written.
// Tier A never replaces nor reuses a key: the firmware may trust an old one.
// Tier B reuses a complete, matching key set, which is how a machine whose
// certificate is already in the MOK list (a reinstall, or vintner secureboot
// request before the install) keeps it.
func (d *deployer) checkSecureBoot() error {
	switch d.SecureBootTier {
	case "", "none":
		return nil
	case "A", "B":
	default:
		return fmt.Errorf("Secure Boot tier %q is not implemented: only none, A and B are", d.SecureBootTier)
	}
	t := d.SecureBootTier
	if !d.uefi {
		return fmt.Errorf("Secure Boot tier %s needs UEFI: BIOS has no Secure Boot", t)
	}
	for _, tool := range []string{"sbsign", "sbverify"} {
		if _, err := d.LookPath(tool); err != nil {
			return fmt.Errorf("%s is not installed (package sbsigntool); Secure Boot tier %s signs and checks with it", tool, t)
		}
	}
	if t == "B" && d.MOKPassword != "" {
		if err := secureboot.ValidatePassword(d.MOKPassword); err != nil {
			return err
		}
	}
	var found []string
	for _, name := range []string{"key.pem", "cert.pem", "cert.der"} {
		if _, err := os.Stat(d.sbPath(name)); err == nil {
			found = append(found, d.sbPath(name))
		}
	}
	switch {
	case len(found) == 0:
		return nil
	case t == "A":
		return fmt.Errorf("%s exists: not replacing a Secure Boot key (the firmware may trust it); move %s away, or install without --secure-boot", found[0], d.SecureBootDir)
	case len(found) != 3:
		return fmt.Errorf("%s holds part of a Secure Boot key set (%d of key.pem, cert.pem and cert.der): not replacing nor using it; move it away", d.SecureBootDir, len(found))
	}
	if err := secureboot.KeyMatchesCert(d.sbPath("key.pem"), d.sbPath("cert.pem")); err != nil {
		return fmt.Errorf("the machine key in %s cannot be used: %w", d.SecureBootDir, err)
	}
	if err := sameCert(d.sbPath("cert.pem"), d.sbPath("cert.der")); err != nil {
		return fmt.Errorf("the machine key in %s cannot be used: %w", d.SecureBootDir, err)
	}
	d.keepKey = true
	return nil
}

// sameCert checks that the DER copy is the PEM certificate.
func sameCert(pemPath, derPath string) error {
	fp, err := secureboot.CertSHA256(pemPath)
	if err != nil {
		return err
	}
	der, err := os.ReadFile(derPath)
	if err != nil {
		return err
	}
	if sum(der) != fp {
		return fmt.Errorf("%s is not the certificate in %s", derPath, pemPath)
	}
	return nil
}

// initSecureBoot creates the machine key and certificate (tiers A and B), or in
// tier B keeps the one checkSecureBoot accepted.
func (d *deployer) initSecureBoot() error {
	if !d.signed() {
		return nil
	}
	if d.keepKey {
		fp, err := secureboot.CertSHA256(d.sbPath("cert.pem"))
		if err != nil {
			return d.fail("secure boot", err)
		}
		d.certFP = fp
		d.rec("kept the machine Secure Boot key in %s (certificate sha256 %s)", d.SecureBootDir, fp)
	} else {
		fp, err := secureboot.GenerateKey(d.SecureBootDir, d.instID)
		if err != nil {
			return d.fail("secure boot", err)
		}
		d.certFP = fp
		where := "the firmware's db"
		if d.tier() == "B" {
			where = "shim's MOK list"
		}
		d.rec("created the machine Secure Boot key and certificate in %s (certificate sha256 %s); the certificate is not in %s yet", d.SecureBootDir, fp, where)
	}
	return d.loadCertDER()
}

func (d *deployer) loadCertDER() error {
	der, err := os.ReadFile(d.sbPath("cert.der"))
	if err != nil {
		return d.fail("secure boot", err)
	}
	d.certDER = der
	return nil
}

// inTarget is a path under the target as the installed system sees it.
func (d *deployer) inTarget(p string) string {
	t := filepath.Clean(d.TargetDir)
	if t == "/" {
		return filepath.ToSlash(p)
	}
	if t == "." { // no target given: a relative path is relative to the root
		if filepath.IsAbs(p) {
			return filepath.ToSlash(p)
		}
		return "/" + filepath.ToSlash(p)
	}
	if rest, ok := strings.CutPrefix(p, t+string(filepath.Separator)); ok {
		return "/" + filepath.ToSlash(rest)
	}
	return filepath.ToSlash(p)
}

// writeDKMSDropIn makes DKMS sign out-of-tree modules with the machine key
// (tiers A and B, WO-0010 5.3). The key is where checkSecureBoot and
// initSecureBoot left it; the drop-in names it and copies nothing.
func (d *deployer) writeDKMSDropIn() error {
	if !d.signed() {
		return nil
	}
	p := filepath.Join(d.TargetDir, filepath.FromSlash(strings.TrimPrefix(secureboot.DKMSDropIn, "/")))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return d.fail("dkms", err)
	}
	content := secureboot.DKMSDropInContent(d.inTarget(d.sbPath("key.pem")), d.inTarget(d.sbPath("cert.der")))
	if err := writeFileSync(p, []byte(content)); err != nil {
		return d.fail("dkms", err)
	}
	d.rec("wrote %s: DKMS signs out-of-tree modules with the machine key", secureboot.DKMSDropIn)
	return nil
}

// signLoader signs the payload's systemd-boot with the machine key (tiers A and
// B), so that stageESP and cutoverESP write the signed copy.
func (d *deployer) signLoader(ctx context.Context) error {
	if !d.signed() {
		return nil
	}
	tmp, err := os.MkdirTemp("", "vintner-loader")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)
	out := filepath.Join(tmp, "loader.efi")
	if err := d.Signer.Sign(ctx, d.Payload.Path(payload.SystemdBoot), out, d.sbPath("key.pem"), d.sbPath("cert.pem")); err != nil {
		return err
	}
	if err := d.Verifier.Verify(ctx, out, d.sbPath("cert.pem")); err != nil {
		return fmt.Errorf("the signed loader: %w", err)
	}
	if d.signedLoader, err = os.ReadFile(out); err != nil {
		return err
	}
	d.rec("signed the loader with the machine key")
	return nil
}

// espContent is what an ESP file must hold: the payload file, the signed copy
// of the loader, or the machine certificate.
func (d *deployer) espContent(payloadRel string) ([]byte, error) {
	switch {
	case payloadRel == espCert:
		if d.certDER == nil {
			return nil, errors.New("the machine certificate is not loaded")
		}
		return d.certDER, nil
	case payloadRel == payload.SystemdBoot && d.signedLoader != nil:
		return d.signedLoader, nil
	}
	return os.ReadFile(d.Payload.Path(payloadRel))
}

// espWant is the sha256 an ESP file must have.
func (d *deployer) espWant(payloadRel string) string {
	switch {
	case payloadRel == espCert:
		return sum(d.certDER)
	case payloadRel == payload.SystemdBoot && d.signedLoader != nil:
		return sum(d.signedLoader)
	}
	return d.Payload.Files[payloadRel]
}

// loadSignedLoader is Resume's way to the signed loader and certificate Install
// wrote: it reads the loader from the ESP and checks its signature against the
// machine certificate, so that verify compares against what is really there.
func (d *deployer) loadSignedLoader(ctx context.Context) error {
	if !d.signed() || !d.uefi {
		return nil
	}
	p := filepath.Join(d.ESPDir, filepath.FromSlash(d.signedLoaderRel()))
	b, err := os.ReadFile(p)
	if err != nil {
		return err
	}
	if err := d.Verifier.Verify(ctx, p, d.sbPath("cert.pem")); err != nil {
		return fmt.Errorf("the loader on the ESP is not signed with this machine's key: %w", err)
	}
	d.signedLoader = b
	return d.loadCertDER()
}

// mokVarDir is where the kernel shows shim's MOK lists from a configuration
// table; only next to the real efivarfs.
func (d *deployer) mokVarDir() string {
	if d.MOKVarDir != "" {
		return d.MOKVarDir
	}
	if d.EFIVarDir == "" || d.EFIVarDir == efivar.DefaultDir {
		return secureboot.DefaultMOKVarDir
	}
	return ""
}

// checkEnrolled runs before the takeover. Tier A refuses to take booting over
// unless the machine certificate is in the firmware's db: with Secure Boot on,
// the firmware would refuse vintner's loader and, once the stock loaders are
// removed, nothing would start. Tier B takes over once the certificate is in
// shim's MOK list or a request for MokManager is written (docs/secure-boot.md
// §8.3). With no efivarfs (installing from outside the machine) the caller must
// say so with SecureBootEnrolled.
func (d *deployer) checkEnrolled() error {
	if d.m == nil || !d.uefi {
		return nil
	}
	switch d.m.SecureBootTier {
	case "A":
		return d.checkDB()
	case "B":
		return d.requestMOK()
	}
	return nil
}

func (d *deployer) checkDB() error {
	if d.SecureBootEnrolled {
		d.rec("Secure Boot: the certificate is taken to be in the firmware's db (--secure-boot-enrolled)")
		return nil
	}
	dir := d.efiVarDir()
	if dir == "" {
		return errors.New("Secure Boot tier A: the firmware's db cannot be read here (no UEFI variables), so it cannot be checked that the machine certificate is enrolled; enroll " + filepath.Join(d.SecureBootDir, "cert.der") + " and run again with --resume --secure-boot-enrolled")
	}
	ok, err := secureboot.CertEnrolled(dir, filepath.Join(d.SecureBootDir, "cert.der"))
	if err != nil {
		return fmt.Errorf("Secure Boot tier A: %w", err)
	}
	if !ok {
		return errors.New("Secure Boot tier A: the machine certificate " + filepath.Join(d.SecureBootDir, "cert.der") + " is not in the firmware's db: vintner's signed loader would be refused and, with the stock loaders removed, nothing would boot. Enroll it (see docs/secure-boot.md), then run again with --resume. The stock boot path is untouched")
	}
	d.rec("Secure Boot: the machine certificate is in the firmware's db")
	return nil
}

// requestMOK makes sure shim will trust the machine certificate: it is in the
// MOK list already, or a request for MokManager is written now.
func (d *deployer) requestMOK() error {
	if d.SecureBootEnrolled {
		d.rec("Secure Boot: the certificate is taken to be in shim's MOK list (--secure-boot-enrolled); no request for MokManager is written")
		return nil
	}
	dir := d.efiVarDir()
	if dir == "" {
		return errors.New("Secure Boot tier B: no UEFI variables here, so no request for MokManager can be written; run again with --resume on the machine itself (booted through UEFI), or with --resume --secure-boot-enrolled once " + filepath.Join(d.SecureBootDir, "cert.der") + " is in the MOK list. The stock boot path is untouched")
	}
	if d.certDER == nil {
		if err := d.loadCertDER(); err != nil {
			return err
		}
	}
	ok, err := secureboot.MOKEnrolled(dir, d.mokVarDir(), d.certDER)
	if err != nil {
		return fmt.Errorf("Secure Boot tier B: %w", err)
	}
	if ok {
		d.rec("Secure Boot: the machine certificate is in shim's MOK list; no request for MokManager is needed")
		return nil
	}
	pw := d.MOKPassword
	if pw == "" {
		if pw, err = secureboot.GeneratePassword(d.Rand); err != nil {
			return err
		}
	}
	if err := secureboot.WriteMOKRequest(dir, d.certDER, pw); err != nil {
		return fmt.Errorf("Secure Boot tier B: the request for MokManager: %w", err)
	}
	d.mokPassword = pw
	d.rec("wrote a request for MokManager to enroll the machine certificate (MokNew, MokAuth, MokTimeout -1): at the next boot, choose Enroll MOK and type the one-time password")
	return nil
}
