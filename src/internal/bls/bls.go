// SPDX-License-Identifier: GPL-2.0-or-later

package bls

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type Entry struct {
	Stem    string
	Counted bool
	Left    int
	Done    int
}

// Parse extracts the stem and boot counter from a filename matching <stem>[+LEFT[-DONE]].efi.
func Parse(filename string) (Entry, bool) {
	ext := filepath.Ext(filename)
	if !strings.EqualFold(ext, ".efi") {
		return Entry{}, false
	}

	base := filename[:len(filename)-len(ext)]

	lastPlus := strings.LastIndexByte(base, '+')
	if lastPlus == -1 {
		return Entry{Stem: base}, true
	}

	counterPart := base[lastPlus+1:]

	leftStr := counterPart
	doneStr := ""

	dashIdx := strings.IndexByte(counterPart, '-')
	if dashIdx != -1 {
		leftStr = counterPart[:dashIdx]
		doneStr = counterPart[dashIdx+1:]
	}

	left, err := strconv.ParseUint(leftStr, 10, 64)
	if err != nil || left > 2147483647 {
		return Entry{Stem: base}, true
	}

	var done uint64
	if dashIdx != -1 {
		done, err = strconv.ParseUint(doneStr, 10, 64)
		if err != nil || done > 2147483647 {
			return Entry{Stem: base}, true
		}
	}

	return Entry{
		Stem:    base[:lastPlus],
		Counted: true,
		Left:    int(left),
		Done:    int(done),
	}, true
}

func (e Entry) Name() string {
	if !e.Counted {
		return e.Stem + ".efi"
	}
	return fmt.Sprintf("%s+%d-%d.efi", e.Stem, e.Left, e.Done)
}

func (e Entry) Bad() bool {
	return e.Counted && e.Left == 0
}

func IsVintnerOrdinary(stem string) (gen int, ok bool) {
	const prefix = "vintner-os-"
	if !strings.HasPrefix(stem, prefix) {
		return 0, false
	}
	val, err := strconv.ParseUint(stem[len(prefix):], 10, 31)
	if err != nil {
		return 0, false
	}
	return int(val), true
}

func IsVintnerRecovery(stem string) (slot string, ok bool) {
	const prefix = "vintner-recovery-"
	if !strings.HasPrefix(stem, prefix) {
		return "", false
	}
	slot = stem[len(prefix):]
	if slot == "a" || slot == "b" {
		return slot, true
	}
	return "", false
}

func Find(dir, stem string) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", err
	}

	var matched []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if parsed, ok := Parse(e.Name()); ok {
			if parsed.Stem == stem {
				matched = append(matched, e.Name())
			}
		}
	}

	if len(matched) == 0 {
		return "", fmt.Errorf("no entry found for stem %q", stem)
	}
	if len(matched) > 1 {
		return "", fmt.Errorf("multiple entries found for stem %q", stem)
	}
	return matched[0], nil
}

// syncPath fsyncs the file or directory at path.
func syncPath(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// RenameSync renames oldpath to newpath and makes the rename durable. On vfat,
// rename(2) writes the new directory entry with start cluster 0 and size 0;
// the real values reach the disk only when the renamed inode itself is
// written back, so syncing the parent directories alone leaves an empty file
// or a directory pointing at the root after a power cut (WO-0007).
func RenameSync(oldpath, newpath string) error {
	if err := os.Rename(oldpath, newpath); err != nil {
		return err
	}
	if err := syncPath(newpath); err != nil {
		return err
	}
	if err := syncPath(filepath.Dir(newpath)); err != nil {
		return err
	}
	if dir := filepath.Dir(oldpath); dir != filepath.Dir(newpath) {
		return syncPath(dir)
	}
	return nil
}

func Rearm(dir, stem string, tries int) error {
	if slot, ok := IsVintnerRecovery(stem); ok && slot == "b" {
		return fmt.Errorf("recovery B never carries a counter")
	}

	match, err := Find(dir, stem)
	if err != nil {
		return err
	}

	parsed, _ := Parse(match)
	if parsed.Counted && parsed.Left == tries && parsed.Done == 0 {
		return nil
	}

	newName := fmt.Sprintf("%s+%d-0.efi", stem, tries)
	if match == newName {
		return nil
	}

	return RenameSync(filepath.Join(dir, match), filepath.Join(dir, newName))
}

func SetBad(dir, stem string) error {
	if slot, ok := IsVintnerRecovery(stem); ok && slot == "b" {
		return fmt.Errorf("recovery B never carries a counter")
	}

	match, err := Find(dir, stem)
	if err != nil {
		return err
	}

	parsed, _ := Parse(match)

	newName := fmt.Sprintf("%s+0-%d.efi", stem, parsed.Done)
	if match == newName {
		return nil
	}

	return RenameSync(filepath.Join(dir, match), filepath.Join(dir, newName))
}
