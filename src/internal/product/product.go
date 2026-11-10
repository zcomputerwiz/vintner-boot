// SPDX-License-Identifier: GPL-2.0-or-later

// Package product reads the product's declaration of itself,
// /etc/vintner/product.conf (WO-0010 4.9). The one key is the name the boot menu
// shows in place of the installed system's PRETTY_NAME, so a product can name
// its entries without changing /etc/os-release. The file format is the one of
// variants.d: one "key=value" per line, blank lines and lines starting with "#"
// ignored, the value the rest of the line, trimmed, with no quoting:
//
//	name   the product's name in the boot menu (optional)
package product

import (
	"fmt"
	"os"
	"strings"

	"github.com/zcomputerwiz/vintner-boot/src/internal/variants"
)

// DefaultFile is where a product declares itself.
const DefaultFile = "/etc/vintner/product.conf"

// Product is what the product declares.
type Product struct {
	// Name is the menu name; empty means the installed PRETTY_NAME is used.
	Name string
}

// Parse reads one declaration.
func Parse(data []byte) (Product, error) {
	var p Product
	seen := map[string]bool{}
	for n, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			return Product{}, fmt.Errorf("line %d: not key=value", n+1)
		}
		key, val = strings.TrimSpace(key), strings.TrimSpace(val)
		if seen[key] {
			return Product{}, fmt.Errorf("line %d: %s given twice", n+1, key)
		}
		seen[key] = true
		switch key {
		case "name":
			if err := variants.ValidateTitle(val); err != nil {
				return Product{}, fmt.Errorf("line %d: name: %w", n+1, err)
			}
			p.Name = val
		default:
			return Product{}, fmt.Errorf("line %d: unknown key %q", n+1, key)
		}
	}
	return p, nil
}

// Load reads the declaration at path. A missing file declares nothing. A file
// that does not validate is an error, naming the file: the product's mistake is
// not silently dropped (as with variants.d).
func Load(path string) (Product, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return Product{}, nil
		}
		return Product{}, err
	}
	p, err := Parse(data)
	if err != nil {
		return Product{}, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}
