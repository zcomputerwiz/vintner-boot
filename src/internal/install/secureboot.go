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
	if d.SecureBootTier == "A" {
		return "A"
	}
	return "none"
}

// sbPath is a file of the machine key set; it is empty in tier none, so that
// the UKI builder signs nothing.
func (d *deployer) sbPath(name string) string {
	if d.tier() != "A" {
		return ""
	}
	return filepath.Join(d.SecureBootDir, name)
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

// checkSecureBoot refuses what tier A cannot do, before anything is written.
func (d *deployer) checkSecureBoot() error {
	switch d.SecureBootTier {
	case "", "none":
		return nil
	case "A":
	default:
		return fmt.Errorf("Secure Boot tier %q is not implemented: only none and A are (WO-0010 5.2 adds B)", d.SecureBootTier)
	}
	if !d.uefi {
		return errors.New("Secure Boot tier A needs UEFI: BIOS has no Secure Boot")
	}
	for _, tool := range []string{"sbsign", "sbverify"} {
		if _, err := d.LookPath(tool); err != nil {
			return fmt.Errorf("%s is not installed (package sbsigntool); Secure Boot tier A signs and checks with it", tool)
		}
	}
	for _, name := range []string{"key.pem", "cert.pem", "cert.der"} {
		if _, err := os.Stat(d.sbPath(name)); err == nil {
			return fmt.Errorf("%s exists: not replacing a Secure Boot key (the firmware may trust it); move %s away, or install without --secure-boot", d.sbPath(name), d.SecureBootDir)
		}
	}
	return nil
}

// initSecureBoot creates the machine key and certificate (tier A).
func (d *deployer) initSecureBoot() error {
	if d.tier() != "A" {
		return nil
	}
	fp, err := secureboot.GenerateKey(d.SecureBootDir, d.instID)
	if err != nil {
		return d.fail("secure boot", err)
	}
	d.certFP = fp
	d.rec("created the machine Secure Boot key and certificate in %s (certificate sha256 %s); the certificate is not in the firmware's db yet", d.SecureBootDir, fp)
	return nil
}

// signLoader signs the payload's systemd-boot with the machine key (tier A),
// so that stageESP and cutoverESP write the signed copy.
func (d *deployer) signLoader(ctx context.Context) error {
	if d.tier() != "A" {
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

// espWant is the sha256 an ESP file must have: the payload's, except for the
// loader in tier A, which is the signed copy.
func (d *deployer) espWant(payloadRel string) string {
	if payloadRel == payload.SystemdBoot && d.signedLoader != nil {
		return sum(d.signedLoader)
	}
	return d.Payload.Files[payloadRel]
}

// loadSignedLoader is Resume's way to the signed loader Install wrote: it
// reads it from the ESP and checks its signature against the machine
// certificate, so that verify compares against what is really there.
func (d *deployer) loadSignedLoader(ctx context.Context) error {
	if d.m == nil || d.m.SecureBootTier != "A" || !d.uefi {
		return nil
	}
	d.SecureBootTier = "A"
	b, err := os.ReadFile(filepath.Join(d.ESPDir, "EFI", "vintner", "systemd-bootx64.efi"))
	if err != nil {
		return err
	}
	if err := d.Verifier.Verify(ctx, filepath.Join(d.ESPDir, "EFI", "vintner", "systemd-bootx64.efi"), d.sbPath("cert.pem")); err != nil {
		return fmt.Errorf("the loader on the ESP is not signed with this machine's key: %w", err)
	}
	d.signedLoader = b
	return nil
}

// checkEnrolled refuses to take booting over in tier A unless the machine
// certificate is in the firmware's db: with Secure Boot on, the firmware would
// refuse vintner's loader and, once the stock loaders are removed, nothing
// would start. With no efivarfs (installing from outside the machine) the
// caller must say so with SecureBootEnrolled.
func (d *deployer) checkEnrolled() error {
	if d.m == nil || d.m.SecureBootTier != "A" || !d.uefi {
		return nil
	}
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
