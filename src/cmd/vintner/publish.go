// SPDX-License-Identifier: GPL-2.0-or-later

package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/zcomputerwiz/vintner-boot/src/internal/publish"
)

func cmdPublish(args []string) {
	fs := flag.NewFlagSet("vintner publish", flag.ExitOnError)
	kernelABI := fs.String("kernel", "", "kernel ABI to publish (required)")
	bootStorage := fs.String("boot-storage", "", "boot storage directory (default: mount on demand)")
	stateDir := fs.String("state-dir", "", "manifest directory (default <boot-storage>/vintner/state)")
	rootDir := fs.String("root-dir", "/", "root filesystem directory")
	lockFile := fs.String("lock-file", "", "lock file path (default /run/vintner/publish.lock)")
	cmdlineFile := fs.String("cmdline-file", "", "kernel cmdline file (default /proc/cmdline)")
	variantsDir := fs.String("variants-dir", "", "the product's variant declarations (default <root-dir>/etc/vintner/variants.d)")

	productFile := fs.String("product-file", "", "the product's name for the menu (default <root-dir>/etc/vintner/product.conf)")

	fs.Parse(args)

	if *kernelABI == "" {
		fmt.Fprintln(os.Stderr, "vintner publish: --kernel <abi> is required")
		usagePublish()
		osExit(2)
		return
	}

	opts := publish.Options{
		KernelABI:      *kernelABI,
		BootStorageDir: *bootStorage,
		StateDir:       *stateDir,
		RootDir:        *rootDir,
		LockFile:       *lockFile,
		CmdlineFile:    *cmdlineFile,
		VariantsDir:    *variantsDir,
		ProductFile:    *productFile,
		Stdout:         os.Stdout,
		Stderr:         os.Stderr,
	}

	_, err := publish.Publish(context.Background(), opts)
	if err != nil {
		fmt.Fprintf(os.Stderr, "vintner publish: %v\n", err)
		osExit(1)
		return
	}
}

func usagePublish() {
	fmt.Fprintln(os.Stderr, "usage: vintner publish --kernel <abi> [--boot-storage DIR] [--state-dir DIR] [--root-dir DIR]")
}
