// SPDX-License-Identifier: GPL-2.0-or-later

// Package publishtest holds fakes for the external parts of the publisher
// (ukify, dpkg, the clock, free space) for tests of code that calls
// publish.Publish. It is not used by the vintner binary.
package publishtest

import (
	"context"
	"encoding/binary"
	"os"
	"time"

	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

// WritePE writes a minimal PE file with the given sections (nil ones are left
// out), enough for the publisher's check of a built image.
func WritePE(path string, osrel, cmdline, uname, initrd []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()

	dos := make([]byte, 64)
	dos[0], dos[1] = 'M', 'Z'
	binary.LittleEndian.PutUint32(dos[60:], 64)
	if _, err := f.Write(dos); err != nil {
		return err
	}
	if _, err := f.Write([]byte("PE\x00\x00")); err != nil {
		return err
	}

	type section struct {
		name string
		data []byte
	}
	var secs []section
	for _, s := range []section{{".osrel", osrel}, {".cmdline", cmdline}, {".uname", uname}, {".initrd", initrd}} {
		if s.data != nil {
			secs = append(secs, s)
		}
	}
	fh := make([]byte, 20)
	binary.LittleEndian.PutUint16(fh[0:], 0x8664)
	binary.LittleEndian.PutUint16(fh[2:], uint16(len(secs)))
	const optSize = 240
	binary.LittleEndian.PutUint16(fh[16:], optSize)
	binary.LittleEndian.PutUint16(fh[18:], 0x0200)
	if _, err := f.Write(fh); err != nil {
		return err
	}
	opt := make([]byte, optSize)
	binary.LittleEndian.PutUint16(opt[0:], 0x20b)
	binary.LittleEndian.PutUint32(opt[108:], 16)
	if _, err := f.Write(opt); err != nil {
		return err
	}
	offset := uint32(64 + 4 + 20 + optSize + len(secs)*40)
	for _, s := range secs {
		h := make([]byte, 40)
		copy(h[0:8], s.name)
		size := uint32(len(s.data))
		binary.LittleEndian.PutUint32(h[8:], size)
		binary.LittleEndian.PutUint32(h[12:], offset)
		binary.LittleEndian.PutUint32(h[16:], size)
		binary.LittleEndian.PutUint32(h[20:], offset)
		if _, err := f.Write(h); err != nil {
			return err
		}
		offset += size
	}
	for _, s := range secs {
		if _, err := f.Write(s.data); err != nil {
			return err
		}
	}
	return nil
}

// UKIBuilder builds a PE that carries what was asked for.
type UKIBuilder struct{}

// Build implements publish.UKIBuilder.
func (UKIBuilder) Build(_ context.Context, req publish.UKIRequest) error {
	osrel, _ := os.ReadFile(req.OSReleasePath)
	initrd, _ := os.ReadFile(req.InitrdPath)
	return WritePE(req.OutputPath, osrel, []byte(req.Cmdline), []byte(req.Uname), initrd)
}

// PackageQuery answers dpkg-query -S.
type PackageQuery struct{ Pkg, Ver string }

// PackageAndVersion implements publish.PackageQuery.
func (q PackageQuery) PackageAndVersion(string) (string, string, error) { return q.Pkg, q.Ver, nil }

// Clock is a fixed clock.
type Clock struct{ T time.Time }

// Now implements publish.Clock.
func (c Clock) Now() time.Time { return c.T }

// DiskChecker reports a fixed amount of free space.
type DiskChecker struct{ Free uint64 }

// FreeBytes implements publish.DiskSpaceChecker.
func (d DiskChecker) FreeBytes(string) (uint64, error) { return d.Free, nil }
