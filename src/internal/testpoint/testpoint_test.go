// SPDX-License-Identifier: GPL-2.0-or-later

//go:build vintner_testpoints

package testpoint

import (
	"os"
	"testing"
)

func TestHitTaggedBuild(t *testing.T) {
	origKmsg := writeKmsg
	origSysrq := triggerSysrq
	defer func() {
		writeKmsg = origKmsg
		triggerSysrq = origSysrq
	}()

	var kmsgCalls []string
	var sysrqCount int

	writeKmsg = func(name string) error {
		kmsgCalls = append(kmsgCalls, name)
		return nil
	}
	triggerSysrq = func() error {
		sysrqCount++
		return nil
	}

	reset := func() {
		ResetCounts()
		kmsgCalls = nil
		sysrqCount = 0
	}

	// 1. A match on the bare name
	t.Run("MatchBareName", func(t *testing.T) {
		reset()
		os.Setenv("VINTNER_TEST_INTERRUPT", "staged-recorded")
		defer os.Unsetenv("VINTNER_TEST_INTERRUPT")

		Hit("staged-recorded")
		if len(kmsgCalls) != 1 || kmsgCalls[0] != "staged-recorded" {
			t.Fatalf("expected kmsg write for staged-recorded, got %v", kmsgCalls)
		}
		if sysrqCount != 1 {
			t.Fatalf("expected sysrq count 1, got %d", sysrqCount)
		}
	})

	// 2. A match on name#2, but not on the first hit
	t.Run("MatchSecondHit", func(t *testing.T) {
		reset()
		os.Setenv("VINTNER_TEST_INTERRUPT", "manifest-written#2")
		defer os.Unsetenv("VINTNER_TEST_INTERRUPT")

		Hit("manifest-written")
		if len(kmsgCalls) != 0 || sysrqCount != 0 {
			t.Fatalf("expected no hit on 1st call, got kmsg=%v sysrq=%d", kmsgCalls, sysrqCount)
		}

		Hit("manifest-written")
		if len(kmsgCalls) != 1 || kmsgCalls[0] != "manifest-written" {
			t.Fatalf("expected hit on 2nd call, got kmsg=%v", kmsgCalls)
		}
		if sysrqCount != 1 {
			t.Fatalf("expected sysrq count 1 on 2nd hit, got %d", sysrqCount)
		}

		// Third hit should not match
		Hit("manifest-written")
		if len(kmsgCalls) != 1 || sysrqCount != 1 {
			t.Fatalf("expected no hit on 3rd call, got kmsg=%v sysrq=%d", kmsgCalls, sysrqCount)
		}
	})

	// 3. No match on a different name
	t.Run("NoMatchDifferentName", func(t *testing.T) {
		reset()
		os.Setenv("VINTNER_TEST_INTERRUPT", "staged-recorded")
		defer os.Unsetenv("VINTNER_TEST_INTERRUPT")

		Hit("different-name")
		if len(kmsgCalls) != 0 || sysrqCount != 0 {
			t.Fatalf("expected no hit on different name, got kmsg=%v sysrq=%d", kmsgCalls, sysrqCount)
		}
	})

	// 4. No match on a name that merely contains the set value
	t.Run("NoMatchSubstring", func(t *testing.T) {
		reset()
		os.Setenv("VINTNER_TEST_INTERRUPT", "renamed")
		defer os.Unsetenv("VINTNER_TEST_INTERRUPT")

		Hit("renamed-extra")
		Hit("pre-renamed")
		if len(kmsgCalls) != 0 || sysrqCount != 0 {
			t.Fatalf("expected no hit on superset names, got kmsg=%v sysrq=%d", kmsgCalls, sysrqCount)
		}

		reset()
		os.Setenv("VINTNER_TEST_INTERRUPT", "staging-written")
		Hit("staging")
		if len(kmsgCalls) != 0 || sysrqCount != 0 {
			t.Fatalf("expected no hit on subset name, got kmsg=%v sysrq=%d", kmsgCalls, sysrqCount)
		}
	})

	// 5. name#0, name#x, and name# must never match (WO-0007a Item 17)
	t.Run("InvalidHashN", func(t *testing.T) {
		invalidCases := []string{
			"staged-recorded#0",
			"staged-recorded#-1",
			"staged-recorded#x",
			"staged-recorded#",
			"staged-recorded#01x",
		}
		for _, tc := range invalidCases {
			reset()
			os.Setenv("VINTNER_TEST_INTERRUPT", tc)
			Hit("staged-recorded")
			Hit("staged-recorded")
			if len(kmsgCalls) != 0 || sysrqCount != 0 {
				t.Fatalf("case %q should never match, got kmsg=%v sysrq=%d", tc, kmsgCalls, sysrqCount)
			}
		}
		os.Unsetenv("VINTNER_TEST_INTERRUPT")
	})
}
