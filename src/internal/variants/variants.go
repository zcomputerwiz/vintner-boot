// SPDX-License-Identifier: GPL-2.0-or-later

// Package variants reads the extra boot entries a product declares in
// /etc/vintner/variants.d/<id>.conf (WO-0010 4.3).
//
// A variant is a command line with extra tokens: the publisher builds one more
// image of each retained generation for it, with no boot counter (D-C), and a
// boot of it changes no boot state (D-D). The file format is one "key=value"
// per line; blank lines and lines starting with "#" are ignored. The value is
// the rest of the line, trimmed, with no quoting. The keys are:
//
//	title   the entry's name in the boot menu (required)
//	tokens  extra kernel command-line tokens, separated by blanks (required)
package variants

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// DefaultDir is where products declare variants.
const DefaultDir = "/etc/vintner/variants.d"

// Token is the command-line key vintner adds to every variant's command line.
const Token = "vintner.variant="

// A variant's id is part of file names, GRUB variable names and the command line.
var idRe = regexp.MustCompile(`^[a-z][a-z0-9_]{0,23}$`)

// Variant is one declared extra boot entry.
type Variant struct {
	ID     string
	Title  string
	Tokens string // the product's tokens, as declared
}

// ValidID reports whether id can name a variant.
func ValidID(id string) bool { return idRe.MatchString(id) }

// Cmdline is the command line of the variant of an image whose command line is
// base: base, the variant's tokens and vintner.variant=<id>.
func (v Variant) Cmdline(base string) string {
	return strings.TrimSpace(base) + " " + strings.Join(strings.Fields(v.Tokens), " ") + " " + Token + v.ID
}

// ValidateTokens refuses the tokens a product must not set: the root
// file system and anything of vintner's own (root= and vintner.* belong to the
// publisher, WO-0010 4.2).
func ValidateTokens(tokens string) error {
	fields := strings.Fields(tokens)
	if len(fields) == 0 {
		return errors.New("no tokens")
	}
	for _, f := range fields {
		switch {
		case f == "root" || strings.HasPrefix(f, "root="):
			return fmt.Errorf("token %q: root= is vintner's", f)
		case strings.HasPrefix(f, "vintner."):
			return fmt.Errorf("token %q: vintner.* tokens are vintner's", f)
		}
		for _, r := range f {
			if r == '"' || r == '\\' || r == '$' || !unicode.IsPrint(r) {
				return fmt.Errorf("token %q: only printable characters other than quote, backslash and $ are allowed", f)
			}
		}
	}
	return nil
}

func validateTitle(title string) error {
	if title == "" {
		return errors.New("no title")
	}
	if len(title) > 64 {
		return errors.New("title longer than 64 bytes")
	}
	for _, r := range title {
		if !unicode.IsPrint(r) {
			return fmt.Errorf("title has a character that is not printable: %q", r)
		}
	}
	return nil
}

// Parse reads one declaration. id is the file's name without ".conf".
func Parse(id string, data []byte) (Variant, error) {
	if !ValidID(id) {
		return Variant{}, fmt.Errorf("invalid id %q: lower case letters, digits and _, starting with a letter, at most 24 characters", id)
	}
	v := Variant{ID: id}
	seen := map[string]bool{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return Variant{}, fmt.Errorf("line %d: not key=value", n+1)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if seen[key] {
			return Variant{}, fmt.Errorf("line %d: %s given twice", n+1, key)
		}
		seen[key] = true
		switch key {
		case "title":
			v.Title = val
		case "tokens":
			v.Tokens = val
		default:
			return Variant{}, fmt.Errorf("line %d: unknown key %q", n+1, key)
		}
	}
	if err := validateTitle(v.Title); err != nil {
		return Variant{}, err
	}
	if err := ValidateTokens(v.Tokens); err != nil {
		return Variant{}, err
	}
	return v, nil
}

// Load reads every *.conf in dir, sorted by id. A missing directory declares
// no variants. Any declaration that does not validate is an error, naming the
// file: a product's mistake is not silently dropped.
func Load(dir string) ([]Variant, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Variant
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".conf") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, err
		}
		v, err := Parse(strings.TrimSuffix(name, ".conf"), data)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", filepath.Join(dir, name), err)
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}
