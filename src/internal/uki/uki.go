// SPDX-License-Identifier: GPL-2.0-or-later

package uki

import (
	"bytes"
	"debug/pe"
	"strconv"
	"strings"
)

// Info contains metadata read from a Unified Kernel Image.
type Info struct {
	OSRelease map[string]string
	OSRel     []byte
	Cmdline   []byte
	Uname     []byte
}

// Read reads metadata from the specified Unified Kernel Image file.
func Read(path string) (*Info, error) {
	f, err := pe.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	info := &Info{
		OSRelease: make(map[string]string),
	}

	for _, sec := range f.Sections {
		switch sec.Name {
		case ".osrel":
			info.OSRel, err = readSection(sec)
			if err != nil {
				return nil, err
			}
			info.OSRelease = parseOSRelease(info.OSRel)
		case ".cmdline":
			info.Cmdline, err = readSection(sec)
			if err != nil {
				return nil, err
			}
		case ".uname":
			info.Uname, err = readSection(sec)
			if err != nil {
				return nil, err
			}
		}
	}

	return info, nil
}

func readSection(sec *pe.Section) ([]byte, error) {
	data, err := sec.Data()
	if err != nil {
		return nil, err
	}

	// Trim to VirtualSize.
	if sec.VirtualSize > 0 && sec.VirtualSize < uint32(len(data)) {
		data = data[:sec.VirtualSize]
	}

	// Remove trailing NULs if they were part of the section
	data = bytes.TrimRight(data, "\x00")

	return data, nil
}

// parseOSRelease parses os-release formatted data into a map.
func parseOSRelease(data []byte) map[string]string {
	m := make(map[string]string)
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		idx := strings.IndexByte(line, '=')
		if idx == -1 {
			continue
		}

		key := strings.TrimSpace(line[:idx])
		val := strings.TrimSpace(line[idx+1:])

		if strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"") {
			unquoted, err := strconv.Unquote(val)
			if err == nil {
				val = unquoted
			}
		} else if strings.HasPrefix(val, "'") && strings.HasSuffix(val, "'") {
			if len(val) >= 2 {
				val = val[1 : len(val)-1]
				val = strings.ReplaceAll(val, "\\'", "'")
				val = strings.ReplaceAll(val, "\\\\", "\\")
			}
		}

		m[key] = val
	}
	return m
}

// SortKey returns the sort key as systemd-boot would use (IMAGE_ID else ID).
func (i *Info) SortKey() string {
	if val, ok := i.OSRelease["IMAGE_ID"]; ok && val != "" {
		return val
	}
	return i.OSRelease["ID"]
}

// Version returns the version string as systemd-boot would use
// (IMAGE_VERSION else VERSION else VERSION_ID else BUILD_ID).
func (i *Info) Version() string {
	if val, ok := i.OSRelease["IMAGE_VERSION"]; ok && val != "" {
		return val
	}
	if val, ok := i.OSRelease["VERSION"]; ok && val != "" {
		return val
	}
	if val, ok := i.OSRelease["VERSION_ID"]; ok && val != "" {
		return val
	}
	return i.OSRelease["BUILD_ID"]
}
