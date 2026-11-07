#!/bin/bash
# SPDX-License-Identifier: GPL-2.0-or-later
#
# Local CI gate: runs what GitHub CI runs, in this Linux checkout, two jobs at
# a time (--jobs), and exits 1 if any job fails. See docs/local-testing.md.
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
#   --jobs N       run N jobs at a time (default 2); each extra slot runs in its
#                  own worktree, ci/out/local/ws<N>, and needs about 2 GiB of RAM
#   --list         print the jobs and exit
#
# Like CI, each job starts without its earlier output, runs with no terminal
# (so nothing can wait at a prompt CI never sees) and is judged by its exit
# status. The job lists come from the workflow files, so they follow CI.
# Results go to ci/out/local/run-all/<time>/: one log per job, summary.tsv.
#
# Needs: a clone in the Linux filesystem, ci/local/check-env.sh passing, and
# sudo without a password (jobs have no terminal to ask on).
# shellcheck disable=SC2004 # $k indexes the slot arrays, like the other indexes here
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

REF="" TIMEOUT_MIN=120 KEEP_OUT=no LIST=no JOBS=2
SELECT=()
while [ $# -gt 0 ]; do
	case "$1" in
		--ref) [ $# -ge 2 ] || usage; REF=$2; shift 2 ;;
		--timeout) [ $# -ge 2 ] || usage; TIMEOUT_MIN=$2; shift 2 ;;
		--keep-out) KEEP_OUT=yes; shift ;;
		--jobs) [ $# -ge 2 ] || usage; JOBS=$2; shift 2 ;;
		--list) LIST=yes; shift ;;
		-h | --help) usage ;;
		-*) usage ;;
		*) SELECT+=("$1"); shift ;;
	esac
done
case "$TIMEOUT_MIN" in '' | *[!0-9]*) die "--timeout takes whole minutes" ;; esac
case "$JOBS" in '' | *[!0-9]* | 0) die "--jobs takes a whole number of at least 1" ;; esac

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

# --- Workspaces ----------------------------------------------------------------------
# Each parallel slot runs jobs in its own checkout, because jobs share state
# that must not be shared between concurrent runs: recovery/out (rebuilt by
# most suites), ci/out/Packages, ci/out/cache (re-downloaded checksums) and the
# loop devices and mounts under ci/out. Slot 1 is this clone; slot k > 1 is a
# git worktree of the same commit under ci/out/local/ws<k>, with a copy of the
# image cache.
REPO=$PWD
COMMIT_FULL=$(git rev-parse HEAD)
ROOTS=("$REPO")
setup_slot() {
	local k=$1 ws=$REPO/ci/out/local/ws$1
	if [ -d "$ws/.git" ] || [ -f "$ws/.git" ]; then
		git -C "$ws" checkout -q --detach "$COMMIT_FULL" || die "cannot update workspace $ws"
		git -C "$ws" reset -q --hard "$COMMIT_FULL"
	else
		git worktree prune
		git worktree add -q --detach "$ws" "$COMMIT_FULL" || die "cannot create workspace $ws"
	fi
	if [ -n "$DIRTY" ]; then
		git diff HEAD --binary | git -C "$ws" apply || die "cannot copy uncommitted changes to $ws"
		git ls-files -z -o --exclude-standard | xargs -0 -r cp --parents -t "$ws"
	fi
	mkdir -p "$ws/ci/out"
	if [ ! -d "$ws/ci/out/cache" ] && [ -d "$REPO/ci/out/cache" ]; then cp -a "$REPO/ci/out/cache" "$ws/ci/out/cache"; fi
	ROOTS[$k]=$ws
}
for ((k = 2; k <= JOBS; k++)); do setup_slot "$k"; done
for ((k = 1; k <= JOBS; k++)); do
	# Package indexes: CI fetches them fresh in every job; a stale one names
	# .deb files the mirror no longer has.
	rm -f "${ROOTS[$k]}/ci/out/Packages" "${ROOTS[$k]}/recovery/out/Packages"
done

# --- Running -------------------------------------------------------------------------
# Unmount and detach whatever an earlier or failed job left under ci/out or
# recovery/out of one slot's checkout. Only paths inside that checkout are touched.
release_leftovers() {
	local root=$1 m l f
	while read -r m; do
		sudo umount "$m" 2>/dev/null || sudo umount -l "$m" || true
	done < <(findmnt -rn -o TARGET | grep -E "^($root/ci/out|$root/recovery/out)/" | sort -r)
	while read -r l f; do
		case "$f" in "$root"/ci/out/* | "$root"/recovery/out/*) sudo losetup -d "$l" || true ;; esac
	done < <(losetup -l -n -O NAME,BACK-FILE 2>/dev/null)
}

fmt_time() { printf '%dm%02ds' $(($1 / 60)) $(($1 % 60)); }

TIMEOUT_S=$((TIMEOUT_MIN * 60))
failed=()
done_n=0
SPID=() SJOB=() SSTART=() STIMEDOUT=()   # per slot: process group, job index, start time, timeout reached
# On Ctrl-C or TERM, jobs run in their own sessions and do not see it: stop them here.
on_signal() {
	trap - INT TERM
	echo "interrupted: stopping the running jobs" >&2
	for k in "${!SPID[@]}"; do
		if [ -n "${SPID[$k]}" ]; then kill -TERM -- "-${SPID[$k]}" 2>/dev/null || true; fi
	done
	sleep 5
	for k in "${!SPID[@]}"; do
		if [ -n "${SPID[$k]}" ]; then kill -KILL -- "-${SPID[$k]}" 2>/dev/null || true; release_leftovers "${ROOTS[$k]}"; fi
	done
	exit 130
}
trap on_signal INT TERM

start_job() { # SLOT JOBINDEX
	local k=$1 i=$2 root=${ROOTS[$1]}
	local name=${NAMES[$i]} cmd=${CMDS[$i]} out=${OUTS[$i]} log=$REPO/$RUN_DIR/${NAMES[$i]}.log
	release_leftovers "$root"
	if [ -n "$out" ] && [ "$KEEP_OUT" = no ]; then
		if findmnt -rn -o TARGET | grep -q "^$root/$out/"; then
			finish_job "$k" "$i" FAIL 0 "could not unmount everything under $out"
			return
		fi
		rm -rf --one-file-system "${root:?}/$out"
	fi
	{ echo "# $name on $COMMIT$DIRTY (slot $k: $root)"; echo "# $cmd"; } > "$log"
	# setsid: no controlling terminal, as in CI, and one process group to
	# stop as a whole on timeout.
	(cd "$root" && exec setsid bash -c "$cmd" </dev/null >>"$log" 2>&1) &
	SPID[$k]=$! SJOB[$k]=$i SSTART[$k]=$(date +%s) STIMEDOUT[$k]=no
	echo "start  $name (slot $k)"
}

finish_job() { # SLOT JOBINDEX STATUS SECONDS [NOTE]
	local k=$1 i=$2 status=$3 secs=$4 note=${5:-}
	done_n=$((done_n + 1))
	printf '%s\t%s\t%s\t%s\n' "${NAMES[$i]}" "$status" "$secs" "$REPO/$RUN_DIR/${NAMES[$i]}.log" >> "$SUMMARY"
	printf '[%d/%d] %-18s %s (%s)%s\n' "$done_n" "${#RUN[@]}" "${NAMES[$i]}" "$status" "$(fmt_time "$secs")" "${note:+ $note}"
	[ "$status" = PASS ] || failed+=("${NAMES[$i]}")
	SPID[$k]=""
}

echo "run-all: ${#RUN[@]} job(s) on $COMMIT$DIRTY, $JOBS at a time; logs in $RUN_DIR"
queue=("${RUN[@]}")
next=0
for ((k = 1; k <= JOBS; k++)); do SPID[$k]=""; done
while :; do
	busy=0
	for ((k = 1; k <= JOBS; k++)); do
		if [ -n "${SPID[$k]}" ]; then
			busy=$((busy + 1))
			pid=${SPID[$k]} start=${SSTART[$k]}
			if kill -0 "$pid" 2>/dev/null; then
				if [ "${STIMEDOUT[$k]}" = no ] && [ $(($(date +%s) - start)) -ge "$TIMEOUT_S" ]; then
					STIMEDOUT[$k]=yes
					kill -TERM -- "-$pid" 2>/dev/null || true
					( sleep 30; kill -KILL -- "-$pid" 2>/dev/null || true ) &
				fi
				continue
			fi
			rc=0
			wait "$pid" || rc=$?
			kill -TERM -- "-$pid" 2>/dev/null || true # anything the job left running
			release_leftovers "${ROOTS[$k]}"
			secs=$(($(date +%s) - start))
			if [ "${STIMEDOUT[$k]}" = yes ]; then status=TIMEOUT
			elif [ "$rc" = 0 ]; then status=PASS
			else status=FAIL; fi
			finish_job "$k" "${SJOB[$k]}" "$status" "$secs"
			busy=$((busy - 1))
		fi
		if [ -z "${SPID[$k]}" ] && [ "$next" -lt "${#queue[@]}" ]; then
			start_job "$k" "${queue[$next]}"
			next=$((next + 1))
			[ -n "${SPID[$k]}" ] && busy=$((busy + 1))
		fi
	done
	if [ "$busy" -eq 0 ] && [ "$next" -ge "${#queue[@]}" ]; then break; fi
	sleep 2
done

echo
column -t -s $'\t' "$SUMMARY" 2>/dev/null || cat "$SUMMARY"
if [ ${#failed[@]} -gt 0 ]; then
	echo "run-all: FAILED on $COMMIT$DIRTY: ${failed[*]}"
	exit 1
fi
echo "run-all: all ${#RUN[@]} job(s) passed on $COMMIT$DIRTY"
