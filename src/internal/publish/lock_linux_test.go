// SPDX-License-Identifier: GPL-2.0-or-later

//go:build linux

package publish

import (
	"path/filepath"
	"testing"
	"time"
)

// A second holder waits for the first instead of failing: overlapping hook runs
// and a bless at boot must serialise (WO-0005 step 2).
func TestAcquireLockWaits(t *testing.T) {
	path := filepath.Join(t.TempDir(), "run", "publish.lock")
	first, err := AcquireLock(path)
	if err != nil {
		t.Fatal(err)
	}

	got := make(chan error, 1)
	go func() {
		second, err := AcquireLock(path)
		if err == nil {
			err = second.Close()
		}
		got <- err
	}()

	select {
	case err := <-got:
		t.Fatalf("second AcquireLock returned (%v) while the first still held the lock", err)
	case <-time.After(200 * time.Millisecond):
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-got:
		if err != nil {
			t.Fatalf("second AcquireLock: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("second AcquireLock did not return after the first released the lock")
	}
}
