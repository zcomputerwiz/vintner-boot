// SPDX-License-Identifier: GPL-2.0-or-later
//go:build !linux

package publish

import "math"

func statfsFreeBytes(path string) (uint64, error) {
	return math.MaxUint64, nil
}
