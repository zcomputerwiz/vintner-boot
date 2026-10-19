// SPDX-License-Identifier: GPL-2.0-or-later

//go:build vintner_testpoints

package testpoint

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
)

var (
	mu     sync.Mutex
	counts = make(map[string]int)

	writeKmsg = func(name string) error {
		f, err := os.OpenFile("/dev/kmsg", os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = fmt.Fprintf(f, "<2>vintner-testpoint: %s\n", name)
		return err
	}

	triggerSysrq = func() error {
		f, err := os.OpenFile("/proc/sysrq-trigger", os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = f.WriteString("b\n")
		return err
	}
)

// Hit checks if name matches the VINTNER_TEST_INTERRUPT specification.
// If matched, it writes to /dev/kmsg, triggers a SysRq reset, and panics on failure.
func Hit(name string) {
	val := os.Getenv("VINTNER_TEST_INTERRUPT")
	if val == "" {
		return
	}

	targetName := val
	targetN := 1
	if base, nStr, ok := strings.Cut(val, "#"); ok {
		targetName = base
		n, err := strconv.Atoi(nStr)
		if err != nil || n <= 0 {
			return
		}
		targetN = n
	}

	mu.Lock()
	counts[name]++
	n := counts[name]
	mu.Unlock()

	if name != targetName || n != targetN {
		return
	}

	if err := writeKmsg(name); err != nil {
		panic(fmt.Sprintf("testpoint %s: write /dev/kmsg: %v", name, err))
	}
	if err := triggerSysrq(); err != nil {
		panic(fmt.Sprintf("testpoint %s: write /proc/sysrq-trigger: %v", name, err))
	}
}

// ResetCounts resets the hit counts (used in tests).
func ResetCounts() {
	mu.Lock()
	defer mu.Unlock()
	counts = make(map[string]int)
}
