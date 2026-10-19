// SPDX-License-Identifier: GPL-2.0-or-later

//go:build !vintner_testpoints

package testpoint

// Hit is a no-op in default builds.
func Hit(name string) {}
