// SPDX-License-Identifier: GPL-2.0-or-later

package secureboot

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf16"

	"github.com/zcomputerwiz/vintner-boot/src/internal/efivar"
)

// Tier B (docs/secure-boot.md §8): shim trusts the machine certificate once it
// is in the Machine Owner Key (MOK) list. Only MokManager adds to that list, at
// the console, when it finds a request: the variables MokNew, MokAuth and
// MokTimeout. This file writes and reads them, as mokutil --import and
// mokutil --timeout -1 do.

// MOK variable attributes: non-volatile, boot services and runtime access.
const mokAttrs = 0x7

// x509CertType is EFI_CERT_X509_GUID, in the byte order of an EFI_GUID.
var x509CertType = guidBytes(0xa5c059a1, 0x94e4, 0x4aa7, [8]byte{0x87, 0xb5, 0xab, 0x15, 0x5c, 0x2b, 0xf0, 0x72})

// shimLockOwner is SHIM_LOCK_GUID, the owner shim and mokutil put on a MOK entry.
var shimLockOwner = guidBytes(0x605dab50, 0xe046, 0x4300, [8]byte{0xab, 0xb6, 0x3d, 0xd8, 0x10, 0xdd, 0x8b, 0x23})

func guidBytes(a uint32, b, c uint16, d [8]byte) []byte {
	g := make([]byte, 16)
	binary.LittleEndian.PutUint32(g[0:], a)
	binary.LittleEndian.PutUint16(g[4:], b)
	binary.LittleEndian.PutUint16(g[6:], c)
	copy(g[8:], d[:])
	return g
}

// MOKSignatureList is the EFI_SIGNATURE_LIST that MokNew holds for one X.509
// certificate (DER): the list header, then one EFI_SIGNATURE_DATA owned by shim.
func MOKSignatureList(der []byte) []byte {
	size := 16 + len(der)
	b := make([]byte, 0, 28+size)
	b = append(b, x509CertType...)
	b = binary.LittleEndian.AppendUint32(b, uint32(28+size)) // SignatureListSize
	b = binary.LittleEndian.AppendUint32(b, 0)               // SignatureHeaderSize
	b = binary.LittleEndian.AppendUint32(b, uint32(size))    // SignatureSize
	b = append(b, shimLockOwner...)
	return append(b, der...)
}

// MOKAuth is MokManager's plain form of MokAuth: SHA-256 over the request and
// the password in UTF-16LE, with no terminator.
func MOKAuth(request []byte, password string) []byte {
	h := sha256.New()
	h.Write(request)
	for _, u := range utf16.Encode([]rune(password)) {
		h.Write([]byte{byte(u), byte(u >> 8)})
	}
	return h.Sum(nil)
}

// ValidatePassword accepts what a person can type at MokManager's prompt: 1 to
// 256 printable ASCII characters (the firmware console has no other input).
func ValidatePassword(p string) error {
	if len(p) < 1 || len(p) > 256 {
		return fmt.Errorf("the MOK password must be 1 to 256 characters, not %d", len(p))
	}
	for _, r := range p {
		if r < 0x21 || r > 0x7e {
			return errors.New("the MOK password must be printable ASCII without spaces: it is typed at the firmware console")
		}
	}
	return nil
}

// passwordAlphabet leaves out characters that are easy to misread (0 O 1 l I).
const passwordAlphabet = "abcdefghijkmnpqrstuvwxyz23456789"

// GeneratePassword returns a random one-time password of 10 characters from
// passwordAlphabet, read from r (crypto/rand when nil).
func GeneratePassword(r io.Reader) (string, error) {
	if r == nil {
		r = rand.Reader
	}
	max := big.NewInt(int64(len(passwordAlphabet)))
	out := make([]byte, 10)
	for i := range out {
		n, err := rand.Int(r, max)
		if err != nil {
			return "", err
		}
		out[i] = passwordAlphabet[n.Int64()]
	}
	return string(out), nil
}

// WriteMOKRequest asks MokManager to enroll the certificate (DER) at the next
// boot, behind password: MokTimeout -1 (MokManager waits for a person instead of
// deleting the request after 10 seconds, ci/mok-chain E2 and E3), MokAuth, and
// MokNew last, because MokNew is what makes shim start MokManager. An older
// request is replaced. It reads the three back.
func WriteMOKRequest(efivarDir string, der []byte, password string) error {
	if err := ValidatePassword(password); err != nil {
		return err
	}
	if len(der) == 0 {
		return errors.New("the certificate is empty")
	}
	req := MOKSignatureList(der)
	auth := MOKAuth(req, password)
	timeout := []byte{0xff, 0xff, 0xff, 0xff} // -1, 32 bits
	vars := []struct {
		name string
		data []byte
	}{{"MokTimeout", timeout}, {"MokAuth", auth}, {"MokNew", req}}
	// An old request goes first, so that no MokNew pairs with a new MokAuth.
	for _, n := range []string{"MokNew", "MokAuth"} {
		if err := efivar.Delete(efivarDir, n, efivar.ShimLock); err != nil && !errors.Is(err, efivar.ErrNotFound) {
			return fmt.Errorf("remove the old %s: %w", n, err)
		}
	}
	for _, v := range vars {
		if err := efivar.Delete(efivarDir, v.name, efivar.ShimLock); err != nil && !errors.Is(err, efivar.ErrNotFound) {
			return fmt.Errorf("remove the old %s: %w", v.name, err)
		}
		if err := efivar.Set(efivarDir, v.name, efivar.ShimLock, mokAttrs, v.data); err != nil {
			return fmt.Errorf("write %s: %w", v.name, err)
		}
	}
	for _, v := range vars {
		_, got, err := efivar.Get(efivarDir, v.name, efivar.ShimLock)
		if err != nil {
			return fmt.Errorf("read %s back: %w", v.name, err)
		}
		if !bytes.Equal(got, v.data) {
			return fmt.Errorf("%s does not read back as written", v.name)
		}
	}
	return nil
}

// MOKRequested reports whether MokNew asks to enroll this certificate (DER).
func MOKRequested(efivarDir string, der []byte) (bool, error) {
	_, data, err := efivar.Get(efivarDir, "MokNew", efivar.ShimLock)
	if errors.Is(err, efivar.ErrNotFound) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return len(der) > 0 && bytes.Contains(data, der), nil
}

// DKMSDropIn is the DKMS configuration that vintner install writes in Secure Boot
// tiers A and B: DKMS signs out-of-tree modules with the machine key, which the
// kernel trusts (docs/secure-boot.md §9).
const DKMSDropIn = "/etc/dkms/framework.conf.d/vintner-secureboot.conf"

// DKMSDropInContent is the drop-in for a machine key kept at keyPath and certPath
// (paths as the installed system sees them). DKMS reads the key where it lies and
// never copies it, so it stays off the ESP and boot storage (R11). DKMS makes a
// key of its own when either file is missing, so the files must exist.
func DKMSDropInContent(keyPath, certPath string) string {
	return "# Written by vintner install (Secure Boot tiers A and B). DKMS signs out-of-tree\n" +
		"# kernel modules with the machine key, which Debian's kernel trusts: through\n" +
		"# shim's MOK list in tier B and through the firmware's db in tier A. The key\n" +
		"# is read where it lies. See docs/secure-boot.md.\n" +
		"mok_signing_key=" + shellQuote(keyPath) + "\n" +
		"mok_certificate=" + shellQuote(certPath) + "\n"
}

// shellQuote quotes s for a shell that sources the drop-in: single quotes, with
// each embedded single quote written as quote, backslash, quote, quote.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// DefaultMOKVarDir is where Linux shows the MOK lists that shim passes in a
// configuration table, whole, when they are too big for a variable.
const DefaultMOKVarDir = "/sys/firmware/efi/mok-variables"

// MOKEnrolled reports whether the certificate (DER) is in the MOK list that shim
// passed to this boot: MokListRT (and MokListRT1, 2, ... when shim split it) in
// efivarfs, or MokListRT in mokVarDir. The list itself (MokList) is boot
// services only and cannot be read from Linux. No list at all counts as not
// enrolled: shim did not start this boot, or the list is empty.
func MOKEnrolled(efivarDir, mokVarDir string, der []byte) (bool, error) {
	if len(der) == 0 {
		return false, errors.New("the certificate is empty")
	}
	if mokVarDir != "" {
		data, err := os.ReadFile(filepath.Join(mokVarDir, "MokListRT"))
		if err == nil && bytes.Contains(data, der) {
			return true, nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return false, err
		}
	}
	for i := 0; ; i++ {
		name := "MokListRT"
		if i > 0 {
			name = fmt.Sprintf("MokListRT%d", i)
		}
		_, data, err := efivar.Get(efivarDir, name, efivar.ShimLock)
		if errors.Is(err, efivar.ErrNotFound) {
			return false, nil
		}
		if err != nil {
			return false, err
		}
		if bytes.Contains(data, der) {
			return true, nil
		}
	}
}
