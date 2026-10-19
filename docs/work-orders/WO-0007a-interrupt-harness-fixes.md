# WO-0007a: Fixes to the publish-interruption harness

- **For:** the agent that delivered WO-0007 (#48).
- **Issued:** 2026-10-19
- **Status:** ready.
- **Branch:** keep working on `wo-0007` (PR #48). First merge `origin/claude/festive-tesla-9ekjn5` into it with a merge commit, not a rebase. That branch carries this work order and the product fix below. Push to `wo-0007`. PR #48 is then the one to merge, and #49 will be closed.

## Why

WO-0007 did its job: it found a real bug, and the explanation in #48 was right.

**The bug.** On Linux vfat, `rename(2)` writes the new directory entry with start cluster 0 and size 0. The real values reach the disk only when the renamed inode itself is written back. The publisher and `bls.Rearm` synced only the parent directories. A power cut after the rename therefore left either a slot directory pointing at the root or an empty UKI. On UEFI, the re-armed os-1 UKI on boot 1 met the same fate, which is how those runs ended in recovery.

**The product fix.** Commit `129e3eb`, already on `claude/festive-tesla-9ekjn5`, adds `bls.RenameSync`. It renames, then fsyncs the renamed path and both parents. The publisher, `Rearm` and `SetBad` all use it. Do not change it. If the cases still fail after it, report the evidence.

**Why the harness needs work.** A review of the harness found checks that can PASS without the evidence WO-0007 requires. This work order fixes them. After it, a PASS must mean what WO-0007 §5 says.

## Rules

The rules of WO-0007 still apply in full:
- compare whole values;
- every check must be able to fail;
- missing evidence is a FAIL;
- evidence is copied from output and sanitised;
- do not adjust expected values.

**Scope:** change only `ci/publish-interrupt/` and `src/internal/testpoint/`, apart from the merge above. If a fix needs anything else, stop and report.

Reuse the strict helpers of `ci/candidate-fallback/inspector.py`:
- `clean_ansi`;
- `mtype` and `mdir` that raise unless the file is not found;
- whole-line matching;
- `identify_booted`.

## Fixes

Line numbers refer to `wo-0007` at `b676aed`. I is `inspector.py`, R is `run.sh`.

### Blocking

1. **P1 testpoint line (I:410).** It must be a whole line: `re.fullmatch(r"\[\s*\d+\.\d+\] vintner-testpoint: " + re.escape(tp), line)` on cleaned lines.
   - The BIOS getty prompt currently prefixes it: `debian-vintner-lifecycle login: [ 13.31] vintner-testpoint: …`. Stop `serial-getty@ttyS0.service` in the case operation before the publish; `ci/lib`'s report service shows how. Do not loosen the match.
   - Require exactly one testpoint line, followed later by the whole line `[<uptime>] sysrq: Resetting`.
   - The operation boot's meta must exist and show that QEMU exited rather than was killed.
2. **Escape codes (I:34, I:200).**
   - `clean_ansi` must remove every escape sequence; P1's evidence currently shows `\x1b[!p\x1b]104\x07\x1b[?7h`. Use `\x1b(?:\[[0-?]*[ -/]*[@-~]|\][^\x07\x1b]*(?:\x07|\x1b\\)|[@-Z\\-_])`.
   - `emit` must replace every control character (`[\x00-\x1f\x7f]`) with a space.
3. **Publish output on the serial console.** Today the operation's output goes to `/tmp/op.log`, and only `OP EXIT` and a 20-line tail reach the console after the script ends. So P1's "no published line" can never fail.
   - In the case operation and the recovery publish, run `update-initramfs … 2>&1 | tee /dev/ttyS0` under `set -o pipefail`, so the exit status is kept.
   - P1, for T1–T6, M1 and M2: the whole line `vintner publish: published generation <p>` must be absent. For G1 and G2 it must be present, before the testpoint line.
   - P6: require the whole lines `=== OP EXIT: 0 ===` and `vintner publish: published generation <n>`.
   - M1 and M2 (WO-0007 §3): the operation's output must have the whole line `vintner publish: staged generation 2`. Before it there must be no output from a step 4 repair. Otherwise the case FAILs with that evidence.
4. **The rest of P1 (I:413–444).**
   - FAIL when the meta file is missing; it defaults to `{}` today.
   - Match whole values only: `generation 2` must not match `generation 22`.
   - For C0, require no testpoint line and the whole published line.
5. **P9 (I:811).**
   - Include the operation boot as well as boot 1, the recovery-publish boot and boot 2.
   - PASS only if every label fully matches `os-\d+`. `unknown`, `grub=…` or a missing log is a FAIL.
6. **Reads (I:43–107).** Errors are swallowed today, so a corrupt staging directory reads as empty and P5 passes. Use the house `mtype` and `mdir`, which raise.
   - Report directory entries outside the tree as anomalies in P8; do not drop them.
   - An unreadable manifest is a FAIL.
   - The GOOD set in the read before the operation must equal exactly `[1]` on the base disk and `[1, 2, 3]` on the garbage-collection disk; otherwise every row of the case FAILs.
7. **P4 (I:531–549).**
   - On both firmwares, check `uefi.sha256` and every `bios.files` entry at every read, including the read before the operation. Each generation must have exactly one UKI, matched by full name.
   - Print full 64-character hashes.
8. **P4 and garbage collection (a clarification of WO-0007).** In G1 and G2 the recovery publish (generation 5) collects generation 2, so its record disappears.
   - An image that was GOOD before the case must stay GOOD, with its recorded hashes, at every read while it has a record.
   - When a later read no longer has its record, it PASSes only if the recovery publish printed the whole line `vintner publish: removed generation <g>`.
   - Write this rule in the README's P4 row.
9. **P5 hard conditions (I:556–712).** These are currently evidence only. Make each a condition of PASS:
   - **All cases:** `next_generation` exactly. The manifest has `firmware: both`, so `grubenv` exists on both runs:
     - C0, T5, T6, M2, G1, G2: `VINTNER_ORDER` exactly, with `p` first, and `VINTNER_T_p == "xxx"`;
     - T1–T4 and M1: `VINTNER_ORDER == ["1"]` and no `VINTNER_T_2`.
   - **C0, BIOS:** `VINTNER_ORDER == ["2", "1"]`.
   - **T4, UEFI:** bless's rule 7 report for generation 2 is required as a whole line, in its console form `[<uptime>] vintner[<pid>]: generation 2 has BIOS counter ""`. Take the exact text from `src/internal/state/state.go`.
   - **T4, BIOS:** bless's rule 6 report (`generation 2 is STAGED and in place but does not verify`) is required as a whole line. The entry `/vintner/bios/os-2/` must be present, compared exactly, not with `startswith`.
   - **M1:** the read after the operation and boot 1's read must agree. A record with `next_generation` 3 after the operation means boot 1 shows it removed, with 3. No record with 2 means boot 1 shows the same.
   - **M2:** the picked copy must be exactly `manifest-a` or `manifest-b`.
   - **G1 and G2:**
     - generation 1 has no record, and its files remain;
     - bless's rule 3 reports for them are required as whole lines;
     - G1 `VINTNER_ORDER == ["4", "3", "2", "1"]`;
     - G2 `VINTNER_ORDER == ["4", "3", "2"]` with no `VINTNER_T_1`;
     - an unreadable `grubenv` is a FAIL.
10. **Default-build check (R:69).** `strings | grep -q` under `pipefail` passes exactly when the strings are present. Use `grep -a -q -F -e VINTNER_TEST_INTERRUPT -e vintner-testpoint "$DEFAULT_BIN"`, where a match fails the run. Add a positive control: the tagged binary must contain both strings.
11. **Final gate (R:391–400).** It counts rows. Instead, build the set {P1…P9} × cases and require exactly one row for each pair: PASS, or OBSERVED for P8. A duplicate or a missing row fails the run.

### Minor

12. **P3.** FAIL when a record existed before the case but its `last_good` cannot be parsed. For the new generation `p`, assert that it had no record before the case.
13. **P7.** The manifest read after the recovery publish must exist, and every image in it must have a kernel package. Compare the lists exactly.
14. **Retained-kernels file.** Read `50vintner-retained-kernels` after every boot, as WO-0007 §3 says. Record a missing file explicitly; do not hide it with `|| true`.
15. **Timeouts.** Kill any boot after 120 seconds (§3), and record each kill in the results evidence.
16. **Exit labels.** Exit code 0 with a `sysrq: Resetting` line is `reset`. Only a boot with a `reboot: Power down` line is `poweroff`.
17. **`#n` parsing.** `name#0`, `name#x` and `name#` must never match. Add a unit test. The default-build test must loop over all 9 test-point names.
18. **Picked-copy regex (I:107).** Anchor it. A missing match is a FAIL in M2.
19. **PR description.** Say that the `renamed` placement split the `if isBIOS` block in `publish.go`.

### Filesystem integrity (added after the first run with the product fix)

20. **`fsck.fat -n` at every read.** Run `fsck.fat -n` on boot storage after every read, using a partition copy or an offset, never a disk you did not create. Keep its output in the case's output directory.
    - P5 FAILs if, at the read after boot 1, the output reports anything other than the dirty bit, for example cross-linked clusters, duplicate entries or a bad directory.
    - P8 lists every such message for every read.
    - **Why:** in run 39249495883 (UEFI, on `129e3eb`), T4's read after boot 1 still listed `/vintner/staging/os-2/` with its three files and `/vintner/staging/vintner-os-2.efi`. The same files were also in `/vintner/bios/os-2/` and `/EFI/Linux/`. If both directory entries point at the same clusters, deleting the staging copy later would free clusters that a live image still uses. Report what `fsck.fat` shows for T4 on both firmwares, and for every other case where staging is not empty. Do not try to fix it.

## Validation before you push

- `bash -n` and `shellcheck -x` on `run.sh`.
- `python3 -m py_compile`, with no `__pycache__` committed.
- In `src/`:
  - `gofmt`, `go vet ./...` and `go test ./...` pass;
  - `go test -tags vintner_testpoints ./internal/testpoint/` passes.
- **A synthetic test of the evaluator**, not committed:
  - a fully correct run gives every row PASS (P8 OBSERVED) on both firmwares;
  - each of these turns its row FAIL: the testpoint text in the middle of a line, a missing `sysrq: Resetting`, a killed operation boot, a published line in T1, no `OP EXIT: 0` in the recovery publish, a boot labelled `unknown`, a different hash for generation 1's BIOS files on a UEFI run, generation 2 collected without its `removed` line, T4 BIOS without the rule 6 line, M1 inconsistent, and a duplicated row.

## Reporting back

- **Results:** both results tables in the README and PR #48, from that PR's own CI run.
- **Findings, with evidence:**
  - any case that still differs from WO-0007 §4 after the product fix;
  - what P8 observed in each case.
- **Anything that cannot be done as written:** say exactly why. Do not adjust expected values.
