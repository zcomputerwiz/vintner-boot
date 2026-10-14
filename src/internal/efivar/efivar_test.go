// SPDX-License-Identifier: GPL-2.0-or-later

package efivar

import (
	"bytes"
	"errors"
	"testing"
)

func TestEfivar_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	name := "TestVar"
	guid := GUID("12345678-1234-1234-1234-123456789abc")
	var attrs uint32 = 0x7 // NV, BS, RT
	data := []byte{0x01, 0x02, 0x03, 0x04}

	err := Set(dir, name, guid, attrs, data)
	if err != nil {
		t.Fatalf("Set failed: %v", err)
	}

	gotAttrs, gotData, err := Get(dir, name, guid)
	if err != nil {
		t.Fatalf("Get failed: %v", err)
	}

	if gotAttrs != attrs {
		t.Errorf("expected attrs %x, got %x", attrs, gotAttrs)
	}

	if !bytes.Equal(gotData, data) {
		t.Errorf("expected data %x, got %x", data, gotData)
	}

	err = Delete(dir, name, guid)
	if err != nil {
		t.Fatalf("Delete failed: %v", err)
	}

	_, _, err = Get(dir, name, guid)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestEfivar_String_RoundTrip(t *testing.T) {
	dir := t.TempDir()

	name := "TestStringVar"
	guid := LoaderInfo
	var attrs uint32 = 0x7 // NV, BS, RT
	strData := "Hello, 世界" // Include non-ASCII

	err := SetString(dir, name, guid, attrs, strData)
	if err != nil {
		t.Fatalf("SetString failed: %v", err)
	}

	gotAttrs, gotStr, err := GetString(dir, name, guid)
	if err != nil {
		t.Fatalf("GetString failed: %v", err)
	}

	if gotAttrs != attrs {
		t.Errorf("expected attrs %x, got %x", attrs, gotAttrs)
	}

	if gotStr != strData {
		t.Errorf("expected string %q, got %q", strData, gotStr)
	}
}

func TestEfivar_NotFound(t *testing.T) {
	dir := t.TempDir()

	_, _, err := Get(dir, "NonExistent", LoaderInfo)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for missing variable, got %v", err)
	}

	err = Delete(dir, "NonExistent", LoaderInfo)
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("expected ErrNotFound for deleting missing variable, got %v", err)
	}
}
