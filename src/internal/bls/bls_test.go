// SPDX-License-Identifier: GPL-2.0-or-later

package bls

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestParse(t *testing.T) {
	tests := []struct {
		filename string
		want     Entry
		ok       bool
	}{
		{"foobar+3.efi", Entry{Stem: "foobar", Counted: true, Left: 3, Done: 0}, true},
		{"foobar+4-0.efi", Entry{Stem: "foobar", Counted: true, Left: 4, Done: 0}, true},
		{"foobar+3-1.efi", Entry{Stem: "foobar", Counted: true, Left: 3, Done: 1}, true},
		{"foobar+0-3.efi", Entry{Stem: "foobar", Counted: true, Left: 0, Done: 3}, true},
		{"foobar+0-4.efi", Entry{Stem: "foobar", Counted: true, Left: 0, Done: 4}, true},
		{"x+deb13-amd64.efi", Entry{Stem: "x+deb13-amd64", Counted: false}, true},
		{"x+deb13+2-1.efi", Entry{Stem: "x+deb13", Counted: true, Left: 2, Done: 1}, true},
		{"FOOBAR.EFI", Entry{Stem: "FOOBAR", Counted: false}, true},
		{"vintner-os-1+2147483647-0.efi", Entry{Stem: "vintner-os-1", Counted: true, Left: 2147483647, Done: 0}, true},
		{"vintner-os-1+2147483648-0.efi", Entry{Stem: "vintner-os-1+2147483648-0", Counted: false}, true}, // INT_MAX overflow
		{"vintner-os-1+0-2147483648.efi", Entry{Stem: "vintner-os-1+0-2147483648", Counted: false}, true}, // INT_MAX overflow in DONE
		{"vintner-os-1+003-001.efi", Entry{Stem: "vintner-os-1", Counted: true, Left: 3, Done: 1}, true},  // leading zeros
		{"not-efi.txt", Entry{}, false},
	}

	for _, tt := range tests {
		t.Run(tt.filename, func(t *testing.T) {
			got, ok := Parse(tt.filename)
			if ok != tt.ok {
				t.Errorf("Parse() ok = %v, want %v", ok, tt.ok)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Parse() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEntry_Name(t *testing.T) {
	tests := []struct {
		entry Entry
		want  string
	}{
		{Entry{Stem: "foobar", Counted: false}, "foobar.efi"},
		{Entry{Stem: "foobar", Counted: true, Left: 3, Done: 0}, "foobar+3-0.efi"},
		{Entry{Stem: "foobar", Counted: true, Left: 0, Done: 4}, "foobar+0-4.efi"},
	}

	for _, tt := range tests {
		t.Run(tt.want, func(t *testing.T) {
			got := tt.entry.Name()
			if got != tt.want {
				t.Errorf("Name() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEntry_Bad(t *testing.T) {
	tests := []struct {
		entry Entry
		want  bool
	}{
		{Entry{Stem: "foobar", Counted: false}, false},
		{Entry{Stem: "foobar", Counted: true, Left: 3, Done: 0}, false},
		{Entry{Stem: "foobar", Counted: true, Left: 0, Done: 4}, true},
		{Entry{Stem: "foobar", Counted: true, Left: 0, Done: 0}, true},
	}

	for _, tt := range tests {
		t.Run(tt.entry.Name(), func(t *testing.T) {
			got := tt.entry.Bad()
			if got != tt.want {
				t.Errorf("Bad() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestIsVintnerOrdinary(t *testing.T) {
	tests := []struct {
		stem    string
		wantGen int
		wantOk  bool
	}{
		{"vintner-os-1", 1, true},
		{"vintner-os-42", 42, true},
		{"vintner-os-0", 0, true},
		{"vintner-os-", 0, false},
		{"vintner-os-abc", 0, false},
		{"other-os-1", 0, false},
		{"vintner-os-1+3-0", 0, false}, // IsVintnerOrdinary is called on stem, not full name
	}

	for _, tt := range tests {
		t.Run(tt.stem, func(t *testing.T) {
			gen, ok := IsVintnerOrdinary(tt.stem)
			if ok != tt.wantOk {
				t.Errorf("IsVintnerOrdinary() ok = %v, want %v", ok, tt.wantOk)
			}
			if gen != tt.wantGen {
				t.Errorf("IsVintnerOrdinary() gen = %v, want %v", gen, tt.wantGen)
			}
		})
	}
}

func TestIsVintnerRecovery(t *testing.T) {
	tests := []struct {
		stem     string
		wantSlot string
		wantOk   bool
	}{
		{"vintner-recovery-a", "a", true},
		{"vintner-recovery-b", "b", true},
		{"vintner-recovery-c", "", false},
		{"vintner-recovery-", "", false},
		{"other-recovery-a", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.stem, func(t *testing.T) {
			slot, ok := IsVintnerRecovery(tt.stem)
			if ok != tt.wantOk {
				t.Errorf("IsVintnerRecovery() ok = %v, want %v", ok, tt.wantOk)
			}
			if slot != tt.wantSlot {
				t.Errorf("IsVintnerRecovery() slot = %v, want %v", slot, tt.wantSlot)
			}
		})
	}
}

func TestFind(t *testing.T) {
	dir := t.TempDir()

	files := []string{
		"vintner-os-1+3-0.efi",
		"vintner-os-2+0-3.efi",
		"vintner-os-2.efi", // Duplicate stem
		"other.txt",
	}

	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(""), 0644); err != nil {
			t.Fatal(err)
		}
	}

	tests := []struct {
		stem    string
		want    string
		wantErr bool
	}{
		{"vintner-os-1", "vintner-os-1+3-0.efi", false},
		{"vintner-os-2", "", true}, // Multiple entries
		{"vintner-os-3", "", true}, // No entries
	}

	for _, tt := range tests {
		t.Run(tt.stem, func(t *testing.T) {
			got, err := Find(dir, tt.stem)
			if (err != nil) != tt.wantErr {
				t.Errorf("Find() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("Find() got = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRearm(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		initial string
		stem    string
		tries   int
		want    string
		wantErr bool
	}{
		{"normal rearm", "vintner-os-1+2-1.efi", "vintner-os-1", 3, "vintner-os-1+3-0.efi", false},
		{"already rearmed", "vintner-os-2+3-0.efi", "vintner-os-2", 3, "vintner-os-2+3-0.efi", false},
		{"uncounted rearm", "vintner-os-3.efi", "vintner-os-3", 3, "vintner-os-3+3-0.efi", false},
		{"recovery a", "vintner-recovery-a+1-2.efi", "vintner-recovery-a", 3, "vintner-recovery-a+3-0.efi", false},
		{"recovery b error", "vintner-recovery-b.efi", "vintner-recovery-b", 3, "", true},
		{"missing", "", "vintner-os-4", 3, "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.initial != "" {
				if err := os.WriteFile(filepath.Join(dir, tt.initial), []byte(""), 0644); err != nil {
					t.Fatal(err)
				}
			}

			err := Rearm(dir, tt.stem, tt.tries)
			if (err != nil) != tt.wantErr {
				t.Errorf("Rearm() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.want != "" {
				if _, err := os.Stat(filepath.Join(dir, tt.want)); os.IsNotExist(err) {
					t.Errorf("Rearm() file %q not found", tt.want)
				}
			}
		})
	}
}

func TestSetBad(t *testing.T) {
	dir := t.TempDir()

	tests := []struct {
		name    string
		initial string
		stem    string
		want    string
		wantErr bool
	}{
		{"set bad from trying", "vintner-os-1+2-1.efi", "vintner-os-1", "vintner-os-1+0-1.efi", false},
		{"already bad", "vintner-os-2+0-3.efi", "vintner-os-2", "vintner-os-2+0-3.efi", false},
		{"set bad from uncounted", "vintner-os-3.efi", "vintner-os-3", "vintner-os-3+0-0.efi", false},
		{"recovery a", "vintner-recovery-a+1-2.efi", "vintner-recovery-a", "vintner-recovery-a+0-2.efi", false},
		{"recovery b error", "vintner-recovery-b.efi", "vintner-recovery-b", "", true},
		{"missing", "", "vintner-os-4", "", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.initial != "" {
				if err := os.WriteFile(filepath.Join(dir, tt.initial), []byte(""), 0644); err != nil {
					t.Fatal(err)
				}
			}

			err := SetBad(dir, tt.stem)
			if (err != nil) != tt.wantErr {
				t.Errorf("SetBad() error = %v, wantErr %v", err, tt.wantErr)
			}

			if tt.want != "" {
				if _, err := os.Stat(filepath.Join(dir, tt.want)); os.IsNotExist(err) {
					t.Errorf("SetBad() file %q not found", tt.want)
				}
			}
		})
	}
}
