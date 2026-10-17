// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !vintner_testpoints

package testpoint

import (
	"os"
	"testing"
)

func TestHitDefaultBuild(t *testing.T) {
	envKey := "VINTNER_" + "TEST_" + "INTERRUPT"
	os.Setenv(envKey, "staged-recorded")
	defer os.Unsetenv(envKey)

	// In default builds, Hit must return for every name without panicking or failing.
	Hit("staged-recorded")
	Hit("other-name")
}
