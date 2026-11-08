// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt"
	"github.com/zcomputerwiz/vintner-boot/src/internal/gpt/gpttest"
	"github.com/zcomputerwiz/vintner-boot/src/internal/payload"
)

func installDisk(t *testing.T, withBootStorage bool) string {
	t.Helper()
	parts := []gpttest.Part{
		{Type: gpt.TypeBIOSBoot, UUID: "2b0f98d3-e62a-48b4-b260-dfdfda5b8b3f", FirstLBA: 2048, LastLBA: 8191},
		{Type: gpt.TypeESP, UUID: "b558256e-e822-4787-9bd2-c4c47dfb54d9", FirstLBA: 8192, LastLBA: 262143},
	}
	if withBootStorage {
		parts = append(parts, gpttest.Part{Type: gpt.TypeBootStorage, UUID: "2d11fdb7-c9b6-475f-8336-ebdb5b830355", FirstLBA: 262144, LastLBA: 262144 + 2097152 - 1})
	}
	parts = append(parts, gpttest.Part{Type: gpt.TypeLinuxRoot, UUID: "fd202e72-0241-4c3b-953d-ffe506aefc6c", FirstLBA: 2359296, LastLBA: 8000000})
	d := gpttest.Image(parts, 8388608)
	fatBS := func(off int64, total uint32) {
		b := make([]byte, 512)
		binary.LittleEndian.PutUint16(b[11:], 512)
		b[13] = 8
		binary.LittleEndian.PutUint16(b[14:], 32)
		b[16] = 2
		binary.LittleEndian.PutUint32(b[32:], total)
		binary.LittleEndian.PutUint32(b[36:], (total/8)/128+1)
		binary.LittleEndian.PutUint32(b[67:], 0x1234ABCD)
		b[510], b[511] = 0x55, 0xAA
		d.WriteAt(b, off)
	}
	fatBS(8192*512, 253952)
	if withBootStorage {
		fatBS(262144*512, 2097152)
	}
	sb := make([]byte, 1024)
	binary.LittleEndian.PutUint16(sb[56:], 0xEF53)
	d.WriteAt(sb, 2359296*512+1024)
	path := filepath.Join(t.TempDir(), "disk.img")
	if err := d.Save(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func installPayload(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	var lines []string
	for _, n := range append([]string{"VERSIONS"}, payload.Required...) {
		p := filepath.Join(dir, filepath.FromSlash(n))
		os.MkdirAll(filepath.Dir(p), 0o755)
		os.WriteFile(p, []byte(n), 0o644)
		s := sha256.Sum256([]byte(n))
		lines = append(lines, hex.EncodeToString(s[:])+"  ./"+n)
	}
	sort.Strings(lines)
	os.WriteFile(filepath.Join(dir, "MANIFEST"), []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	return dir
}

func TestInstallCheckAcceptsAGoodLayout(t *testing.T) {
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--check", "--disk", installDisk(t, true), "--firmware", "both", "--payload-dir", installPayload(t)}, &out, &errb)
	if rc != 0 {
		t.Fatalf("rc %d, stderr %q", rc, errb.String())
	}
	for _, want := range []string{"boot storage: partition 3, PARTUUID 2d11fdb7-c9b6-475f-8336-ebdb5b830355", "layout and payloads are accepted"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("stdout lacks %q: %s", want, out.String())
		}
	}
}

func TestInstallCheckRefusesWithTheRule(t *testing.T) {
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--check", "--disk", installDisk(t, false), "--firmware", "both", "--payload-dir", installPayload(t)}, &out, &errb)
	if rc != 1 {
		t.Fatalf("rc %d, want 1", rc)
	}
	if !strings.Contains(errb.String(), "refused: L2:") || strings.Contains(out.String(), "accepted") {
		t.Errorf("stderr %q stdout %q", errb.String(), out.String())
	}
}

func TestInstallCheckRefusesAnUnverifiedPayload(t *testing.T) {
	dir := installPayload(t)
	os.WriteFile(filepath.Join(dir, "uefi", "loader.conf"), []byte("edited"), 0o644)
	var out, errb bytes.Buffer
	rc := runInstall([]string{"--check", "--disk", installDisk(t, true), "--firmware", "both", "--payload-dir", dir}, &out, &errb)
	if rc != 1 || !strings.Contains(errb.String(), "differs from its MANIFEST hash") {
		t.Errorf("rc %d stderr %q", rc, errb.String())
	}
}

func TestInstallUsage(t *testing.T) {
	var out, errb bytes.Buffer
	if rc := runInstall([]string{"--check"}, &out, &errb); rc != 2 {
		t.Errorf("no --disk: rc %d", rc)
	}
	errb.Reset()
	if rc := runInstall([]string{"--disk", "x", "--firmware", "both"}, &out, &errb); rc != 2 || !strings.Contains(errb.String(), "not implemented") {
		t.Errorf("without --check: rc %d, stderr %q", rc, errb.String())
	}
}
