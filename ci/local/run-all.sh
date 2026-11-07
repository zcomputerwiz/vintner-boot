#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Local CI gate: runs what GitHub CI runs, in this Linux checkout, one job at
# a time, and exits 1 if any job fails. See docs/local-testing.md.
#
# Usage: ci/local/run-all.sh [options] [job|group ...]
#   job     a job of .github/workflows/qemu-tests.yml (publish-uefi, ...),
#           of qemu-baseline.yml (seabios, ovmf-sb-off, ovmf-sb-on), or of
#           lint.yml (shellcheck, go, graphics-theme, debian-package)
#   group   lint, baseline, qemu, or all (the default)
# Options:
#   --ref REF      test origin/REF: fetch, then switch this clone to it
#                  (detached) first; the working tree must be clean
#   --timeout MIN  limit per job, in minutes (default 120)
#   --keep-out     keep each job's earlier output instead of deleting it first
#   --list         print the jobs and exit
#
# Like CI, each job starts without its earlier output, runs with no terminal
# (so nothing can wait at a prompt CI never sees) and is judged by its exit
# status. The job lists come from the workflow files, so they follow CI.
# Results go to ci/out/local/run-all/<time>/: one log per job, summary.tsv.
#
# Needs: a clone in the Linux filesystem, ci/local/check-env.sh passing, and
# sudo without a password (jobs have no terminal to ask on).
set -euo pipefail

# Run from a copy, so that --ref can switch the checkout under us, and so a
# ref older than this script can still be tested.
if [ -z "${VINTNER_RUNALL_COPY:-}" ]; then
	repo=$(cd "$(dirname "$0")/../.." && pwd)
	copy=$(mktemp "${TMPDIR:-/tmp}/vintner-run-all.XXXXXX")
	cp "$0" "$copy"
	VINTNER_RUNALL_COPY=$copy VINTNER_RUNALL_REPO=$repo exec bash "$copy" "$@"
fi
cd "$VINTNER_RUNALL_REPO"
trap 'rm -f "$VINTNER_RUNALL_COPY"' EXIT

usage() { sed -n '/^# Usage:/,/^#$/p' "$VINTNER_RUNALL_COPY" | sed 's/^# \{0,1\}//' >&2; exit 2; }
die() { echo "run-all: $*" >&2; exit 1; }

REF="" TIMEOUT_MIN=120 KEEP_OUT=no LIST=no
SELECT=()
while [ $# -gt 0 ]; do
	case "$1" in
		--ref) [ $# -ge 2 ] || usage; REF=$2; shift 2 ;;
		--timeout) [ $# -ge 2 ] || usage; TIMEOUT_MIN=$2; shift 2 ;;
		--keep-out) KEEP_OUT=yes; shift ;;
		--list) LIST=yes; shift ;;
		-h | --help) usage ;;
		-*) usage ;;
		*) SELECT+=("$1"); shift ;;
	esac
done
case "$TIMEOUT_MIN" in '' | *[!0-9]*) die "--timeout takes whole minutes" ;; esac

case "$PWD" in
	/mnt/[a-z]/*) die "this checkout is on a Windows drive ($PWD); run the gate from a clone in the Linux filesystem (docs/local-testing.md)" ;;
esac

# --- Preconditions (before --ref: an older ref may not have check-env.sh) ----------
if [ "$LIST" = no ]; then
	ci/local/check-env.sh >/dev/null || { ci/local/check-env.sh | grep -E '^FAIL'; die "environment incomplete (ci/local/check-env.sh)"; }
	sudo -n true 2>/dev/null || die "sudo needs a password, and jobs run with no terminal to ask on: allow this user sudo without a password in this test distribution (docs/local-testing.md)"
	mkdir -p ci/out/local
	exec 9>ci/out/local/run-all.lock
	flock -n 9 || die "another run-all is running in this clone"
fi

if [ -n "$REF" ]; then
	if ! git diff --quiet || ! git diff --cached --quiet; then die "--ref needs a clean working tree"; fi
	git fetch -q origin
	git switch -q --detach "origin/$REF"
fi

# --- Jobs -------------------------------------------------------------------------
# NAMES[i] runs CMDS[i]; OUTS[i] is the output directory CI uploads (deleted
# before the job runs); GROUPS_[i] is lint, baseline or qemu.
NAMES=() CMDS=() OUTS=() GROUPS_=()
add_job() { NAMES+=("$1"); CMDS+=("$2"); OUTS+=("$3"); GROUPS_+=("$4"); }

# lint.yml, step by step. Keep in step with that file.
add_job shellcheck "git ls-files -z '*.sh' | xargs -0 -r shellcheck && ! git ls-files | grep -E '(^|/)ci/out/|[.]key\$|[.]fd\$'" "" lint
add_job go "cd src && test -z \"\$(gofmt -l .)\" && go vet ./... && go test -v ./... && go test -tags vintner_testpoints ./internal/testpoint/ && CGO_ENABLED=0 go build -o /dev/null ./cmd/vintner" "" lint
add_job graphics-theme "bash ci/graphics/test-plymouth-theme.sh" "" lint
# dpkg-buildpackage writes into the source tree and its parent: build in a
# throwaway worktree of HEAD so neither is this clone.
add_job debian-package "d=ci/out/local/debian-package; git worktree prune; git worktree add -q --detach \"\$d/src\" HEAD && (cd \"\$d/src\" && dpkg-buildpackage -us -uc -b) && lintian --fail-on error \"\$d\"/*.changes; rc=\$?; git worktree remove --force \"\$d/src\"; exit \$rc" ci/out/local/debian-package lint

# The matrices of qemu-baseline.yml and qemu-tests.yml: "- name:"/"- target:"
# starts an entry, then "run:"/"script:" and "out:"; the matrix ends at "steps:".
parse_matrix() {
	awk '
		/^[[:space:]]*steps:/ { exit }
		match($0, /^[[:space:]]*- (name|target):[[:space:]]*/) {
			if (n != "") print n "\t" r "\t" o
			n = substr($0, RLENGTH + 1); r = ""; o = ""; next
		}
		match($0, /^[[:space:]]*(run|script):[[:space:]]*/) { r = substr($0, RLENGTH + 1); next }
		match($0, /^[[:space:]]*out:[[:space:]]*/) { o = substr($0, RLENGTH + 1); next }
		END { if (n != "") print n "\t" r "\t" o }
	' "$1"
}
while IFS=$'\t' read -r name script _; do
	[ -n "$script" ] || die "no script for $name in qemu-baseline.yml"
	add_job "$name" "./$script" "ci/out/$name" baseline
done < <(parse_matrix .github/workflows/qemu-baseline.yml)
while IFS=$'\t' read -r name run out; do
	if [ -z "$run" ] || [ -z "$out" ]; then die "no run or out for $name in qemu-tests.yml"; fi
	add_job "$name" "$run" "$out" qemu
done < <(parse_matrix .github/workflows/qemu-tests.yml)

if [ "$LIST" = yes ]; then
	for i in "${!NAMES[@]}"; do printf '%-9s %-18s %s\n' "${GROUPS_[$i]}" "${NAMES[$i]}" "${CMDS[$i]}"; done
	exit 0
fi

[ ${#SELECT[@]} -gt 0 ] || SELECT=(all)
RUN=()
for s in "${SELECT[@]}"; do
	found=no
	for i in "${!NAMES[@]}"; do
		if [ "$s" = all ] || [ "$s" = "${GROUPS_[$i]}" ] || [ "$s" = "${NAMES[$i]}" ]; then
			found=yes
			case " ${RUN[*]} " in *" $i "*) ;; *) RUN+=("$i") ;; esac
		fi
	done
	[ "$found" = yes ] || die "unknown job or group '$s' (see --list)"
done

RUN_DIR="ci/out/local/run-all/$(date +%Y%m%dT%H%M%S)"
mkdir -p "$RUN_DIR"
SUMMARY="$RUN_DIR/summary.tsv"
COMMIT=$(git rev-parse --short HEAD)
DIRTY=""
git diff --quiet HEAD -- || DIRTY=" (with uncommitted changes)"
printf 'job\tstatus\tseconds\tlog\n' > "$SUMMARY"

# Package indexes shared between jobs: CI fetches them fresh in every job; a
# stale one names .deb files the mirror no longer has.
rm -f ci/out/Packages recovery/out/Packages

# --- Running -------------------------------------------------------------------------
# Unmount and detach whatever an earlier or failed job left under ci/out or
# recovery/out. Only paths inside this clone are touched.
release_leftovers() {
	local m l f
	while read -r m; do
		sudo umount "$m" 2>/dev/null || sudo umount -l "$m" || true
	done < <(findmnt -rn -o TARGET | grep -E "^($PWD/ci/out|$PWD/recovery/out)/" | sort -r)
	while read -r l f; do
		case "$f" in "$PWD"/ci/out/* | "$PWD"/recovery/out/*) sudo losetup -d "$l" || true ;; esac
	done < <(losetup -l -n -O NAME,BACK-FILE 2>/dev/null)
}

fmt_time() { printf '%dm%02ds' $(($1 / 60)) $(($1 % 60)); }

# A job runs in its own session, so Ctrl-C does not reach it: stop it here.
pid=""
on_signal() {
	trap - INT TERM
	if [ -n "$pid" ]; then
		echo "interrupted: stopping the running job" >&2
		kill -TERM -- "-$pid" 2>/dev/null || true
		sleep 5
		kill -KILL -- "-$pid" 2>/dev/null || true
		release_leftovers
	fi
	exit 130
}
trap on_signal INT TERM

TIMEOUT_S=$((TIMEOUT_MIN * 60))
failed=()
n=0
echo "run-all: ${#RUN[@]} job(s) on $COMMIT$DIRTY; logs in $RUN_DIR"
for i in "${RUN[@]}"; do
	n=$((n + 1))
	name=${NAMES[$i]} cmd=${CMDS[$i]} out=${OUTS[$i]}
	log="$RUN_DIR/$name.log"
	printf '[%d/%d] %-18s ' "$n" "${#RUN[@]}" "$name"

	release_leftovers
	if [ -n "$out" ] && [ "$KEEP_OUT" = no ]; then
		if findmnt -rn -o TARGET | grep -q "^$PWD/$out/"; then
			echo "FAIL (could not unmount everything under $out)"
			printf '%s\tFAIL\t0\t%s\n' "$name" "$log" >> "$SUMMARY"
			failed+=("$name")
			continue
		fi
		rm -rf --one-file-system "$out"
	fi

	start=$(date +%s)
	{ echo "# $name on $COMMIT$DIRTY"; echo "# $cmd"; } > "$log"
	# setsid: no controlling terminal, as in CI, and one process group to
	# stop as a whole on timeout.
	setsid bash -c "$cmd" </dev/null >>"$log" 2>&1 &
	pid=$!
	timed_out=no
	while kill -0 "$pid" 2>/dev/null; do
		if [ $(($(date +%s) - start)) -ge "$TIMEOUT_S" ]; then
			timed_out=yes
			kill -TERM -- "-$pid" 2>/dev/null || true
			sleep 30
			kill -KILL -- "-$pid" 2>/dev/null || true
			break
		fi
		sleep 5
	done
	rc=0
	wait "$pid" || rc=$?
	kill -TERM -- "-$pid" 2>/dev/null || true # anything the job left running
	release_leftovers
	secs=$(($(date +%s) - start))

	if [ "$timed_out" = yes ]; then
		status=TIMEOUT
	elif [ "$rc" = 0 ]; then
		status=PASS
	else
		status=FAIL
	fi
	printf '%s\t%s\t%s\t%s\n' "$name" "$status" "$secs" "$log" >> "$SUMMARY"
	echo "$status ($(fmt_time "$secs"))"
	[ "$status" = PASS ] || failed+=("$name")
done

echo
column -t -s $'\t' "$SUMMARY" 2>/dev/null || cat "$SUMMARY"
if [ ${#failed[@]} -gt 0 ]; then
	echo "run-all: FAILED on $COMMIT$DIRTY: ${failed[*]}"
	exit 1
fi
echo "run-all: all ${#RUN[@]} job(s) passed on $COMMIT$DIRTY"
