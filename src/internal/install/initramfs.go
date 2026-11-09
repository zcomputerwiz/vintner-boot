// SPDX-License-Identifier: GPL-2.0-or-later

package install

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// initramfsInputs are the directories whose files go into an initramfs: the
// hooks, scripts and configuration of initramfs-tools.
var initramfsInputs = []string{
	"etc/initramfs-tools",
	"usr/share/initramfs-tools",
	"usr/lib/initramfs-tools",
}

// newestInitramfsInput returns the newest modification time under the
// initramfs-tools directories of target, and the file it belongs to. Missing
// directories are ignored.
func newestInitramfsInput(target string) (time.Time, string, error) {
	var newest time.Time
	var which string
	for _, rel := range initramfsInputs {
		root := filepath.Join(target, rel)
		err := filepath.WalkDir(root, func(p string, de fs.DirEntry, err error) error {
			if err != nil {
				if p == root && os.IsNotExist(err) {
					return filepath.SkipDir
				}
				return err
			}
			if de.IsDir() {
				return nil
			}
			info, err := de.Info()
			if err != nil {
				return err
			}
			if info.ModTime().After(newest) {
				newest, which = info.ModTime(), p
			}
			return nil
		})
		if err != nil && !os.IsNotExist(err) {
			return time.Time{}, "", err
		}
	}
	return newest, which, nil
}

// checkInitramfsFresh refuses an initramfs that is older than the newest
// initramfs-tools hook or configuration file of the target: a system installed
// by copying a live one carries the live system's initramfs, which lacks what
// the installed hooks add (WO-0010 3.5). With update set it runs
// update-initramfs -u -k <abi> in the target instead (and checks again).
func checkInitramfsFresh(ctx context.Context, f FullOptions, abi string) error {
	initrd := filepath.Join(f.TargetDir, "boot", "initrd.img-"+abi)
	fresh := func() (string, error) {
		st, err := os.Stat(initrd)
		if err != nil {
			return "", err
		}
		newest, which, err := newestInitramfsInput(f.TargetDir)
		if err != nil {
			return "", err
		}
		if !newest.IsZero() && st.ModTime().Before(newest) {
			return fmt.Sprintf("%s (%s) is older than %s (%s)", initrd, st.ModTime().UTC().Format(time.RFC3339), which, newest.UTC().Format(time.RFC3339)), nil
		}
		return "", nil
	}
	stale, err := fresh()
	if err != nil {
		return err
	}
	if stale == "" {
		return nil
	}
	if !f.UpdateInitramfs {
		return fmt.Errorf("the initramfs is stale: %s; run update-initramfs -u -k %s in the target, or run vintner install with --update-initramfs", stale, abi)
	}
	args := []string{"-u", "-k", abi}
	name := "update-initramfs"
	if f.TargetDir != "/" {
		args = append([]string{f.TargetDir, name}, args...)
		name = "chroot"
	}
	fmt.Fprintf(f.Log, "the initramfs is stale: %s\nrunning update-initramfs -u -k %s\n", stale, abi)
	if out, err := f.Runner.Run(ctx, name, args...); err != nil {
		return fmt.Errorf("update-initramfs: %w: %s", err, out)
	}
	if stale, err = fresh(); err != nil {
		return err
	} else if stale != "" {
		return fmt.Errorf("the initramfs is still stale after update-initramfs: %s", stale)
	}
	return nil
}
