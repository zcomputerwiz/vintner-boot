// SPDX-License-Identifier: GPL-2.0-or-later

package status

import (
	"bufio"
	"errors"
	"io/fs"
	"os"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/manifest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/secureboot"
)

// The states of DKMS's signing configuration.
const (
	signingOK      = "ok"      // DKMS signs with this installation's machine key
	signingBlocked = "blocked" // it does not
	signingUnknown = "unknown" // it cannot be told from here (no permission to read the key)
)

// unquote undoes the shell quoting of a drop-in value: single quotes (with the
// the sequence quote, backslash, quote, quote for a quote inside), double quotes,
// or none.
func unquote(v string) string {
	v = strings.TrimSpace(v)
	if len(v) >= 2 && v[0] == '\'' && v[len(v)-1] == '\'' {
		return strings.ReplaceAll(v[1:len(v)-1], `'\''`, `'`)
	}
	if len(v) >= 2 && v[0] == '"' && v[len(v)-1] == '"' {
		return v[1 : len(v)-1]
	}
	return v
}

// dkmsSigning reads the drop-in and says whether DKMS signs with this
// installation's machine key: the key and certificate named in it are readable,
// the key belongs to the certificate, and the certificate is the one in the
// manifest. The reason says what is wrong. DKMS makes a key pair of its own where
// a file is missing, so a drop-in that points at nothing is worse than none. The
// key is root's: from another user the answer is unknown, not blocked.
func dkmsSigning(m *manifest.Manifest, dropIn string) (state, reason string) {
	f, err := os.Open(dropIn)
	if err != nil {
		return signingBlocked, "DKMS is not configured to sign with the machine key (" + dropIn + " is missing)"
	}
	defer f.Close()
	var key, cert string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimPrefix(strings.TrimSpace(sc.Text()), "export ")
		if k, v, found := strings.Cut(line, "="); found {
			switch strings.TrimSpace(k) {
			case "mok_signing_key":
				key = unquote(v)
			case "mok_certificate":
				cert = unquote(v)
			}
		}
	}
	if key == "" || cert == "" {
		return signingBlocked, "DKMS is not configured to sign with the machine key (" + dropIn + " names no mok_signing_key or mok_certificate)"
	}
	if err := secureboot.KeyMatchesCert(key, cert); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return signingUnknown, "the machine key " + key + " is not readable by this user, so it cannot be checked that DKMS signs with it"
		}
		if errors.Is(err, fs.ErrNotExist) {
			return signingBlocked, "DKMS's signing key " + key + " or certificate " + cert + " is missing: DKMS would make a key of its own"
		}
		return signingBlocked, "DKMS's signing key " + key + " does not belong to its certificate " + cert + ": " + err.Error()
	}
	fp, err := secureboot.CertFileSHA256(cert)
	if err != nil {
		return signingBlocked, "DKMS's certificate " + cert + " cannot be read: " + err.Error()
	}
	if m.Signing == nil || fp != m.Signing.CertSHA256 {
		return signingBlocked, "DKMS's certificate " + cert + " is not the machine certificate of this installation"
	}
	return signingOK, ""
}
