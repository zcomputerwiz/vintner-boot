// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !vintner_testpoints

package testpoint

import (
	"os"
	"testing"
)

func TestHitDefaultBuild(t *testing.T) {
	envKey := "VINTNER_" + "TEST_" + "INTERRUPT"
	testpoints := []string{
		"staged-recorded",
		"staging-written",
		"staging-verified",
		"renamed",
		"grubenv-written",
		"trying-recorded",
		"gc-grubenv-written",
		"gc-files-removed",
		"gc-record-removed",
		"manifest-written",
	}

	for _, name := range testpoints {
		os.Setenv(envKey, name)
		Hit(name)
		Hit("other-name")
	}
	os.Unsetenv(envKey)
}
