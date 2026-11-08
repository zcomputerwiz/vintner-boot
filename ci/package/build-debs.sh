#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
# Builds the vintner .deb files from the committed tree (plus staged changes) into
# DIR, with `dpkg-buildpackage -us -uc -b`, and records the tree they were built
# from in DIR/SOURCE_TREE. Needs a Debian 13 host with the build dependencies of
# debian/control (CI builds them in a debian:trixie container instead and passes the
# result to the rows as ci/out/vintner-debs; see docs/packaging.md).
# Usage: ci/package/build-debs.sh DIR
#        ci/package/build-debs.sh --tree   (print the tree id the packages would be built from)
# VINTNER_RECOVERY_DIR (absolute): a directory holding vmlinuz, initrd.img and
# versions.txt to use instead of downloading the recovery packages.
set -euo pipefail
cd "$(dirname "$0")/../.."

# stash create is empty on a clean tree, then HEAD is the tree to build.
TREE=$(git stash create 2>/dev/null || true)
[ -n "$TREE" ] || TREE=$(git rev-parse HEAD)
if [ "${1:-}" = --tree ]; then
	git rev-parse "$TREE^{tree}"
	exit 0
fi

DIR=${1:?usage: $0 DIR}
mkdir -p "$DIR"
DIR=$(cd "$DIR" && pwd)

# shellcheck source=/dev/null
. /etc/os-release
if [ "${VERSION_CODENAME:-}" != trixie ]; then
	echo "build-debs: this host is ${VERSION_CODENAME:-unknown}, not Debian 13 (trixie); the package builds with Go 1.24 (debian/control)." >&2
	echo "build-debs: build in a debian:trixie container, or set VINTNER_DEBS to a directory that holds the .deb files." >&2
	exit 1
fi

WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT
mkdir "$WORK/src"
git archive "$TREE" | tar -x -C "$WORK/src"

rm -f "$DIR"/vintner-*.deb
(cd "$WORK/src" && dpkg-buildpackage -us -uc -b >"$WORK/build.log" 2>&1) || {
	tail -n 40 "$WORK/build.log" >&2
	echo "build-debs: dpkg-buildpackage failed" >&2
	exit 1
}
cp "$WORK"/vintner-*.deb "$DIR/"
git rev-parse "$TREE^{tree}" > "$DIR/SOURCE_TREE"
ls -l "$DIR"/vintner-*.deb
