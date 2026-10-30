# WO-0007b: Interruption harness for the two phase 4 design fixes

- **For:** the agent that delivered WO-0007 and WO-0007a (`ci/publish-interrupt/`).
- **Issued:** 2026-10-30
- **Status:** open.
- **Branch:** create `wo-0007b` from `origin/claude/festive-tesla-9ekjn5` (it carries the product change below). Open a PR against `main`. Do not rebase; merge `origin/main` with a merge commit if you need to.

## Why

WO-0007 found two design gaps, and the product now closes both. The harness still encodes the old behaviour, so `interrupt-uefi` and `interrupt-bios` fail on T4, G1 and G2. This work order brings the harness to the new behaviour and adds one case.

**Product changes (already on the branch; do not change them, and report evidence if a case still fails):**

1. **Arming (rule 1b).** A `STAGED` BIOS record whose slot files are in place and match the record, with the generation neither in `VINTNER_ORDER` nor holding `VINTNER_T_<g>`, was interrupted between the rename and the `grubenv` write. Reconciliation now finishes the transition: one `grubenv` write sets `VINTNER_T_<g>` and prepends `g` to `VINTNER_ORDER`; the record becomes `TRYING`. With firmware `both`, the UKI must also match. The publisher, `vintner bless` and `vintner reconcile` all apply it and print the line `ACTION: arm_bios generation <g>` (on the console, after the journald prefix: `[<uptime>] vintner[<pid>]: ACTION: arm_bios generation <g>` for bless).
2. **Garbage-collection order.** For each collected generation: (a) BIOS `grubenv` write (drop from `VINTNER_ORDER`, unset `VINTNER_T_<g>`), test point `gc-grubenv-written`; (b) remove the BIOS slot directory and the UKI, then `syscall.Sync()`, test point `gc-files-removed` (new); (c) remove the record, test point `gc-record-removed`; then print `vintner publish: removed generation <g>`. The record goes last, so an interrupted collection is repeated by the next publish.

`schema/state-machine.md` has the rules. `src/internal/state/state.go` has the exact report texts.

## Rules

The rules of WO-0007 and WO-0007a still apply in full: compare whole values; every check must be able to fail; missing evidence is a FAIL; evidence is copied from output and sanitised; do not adjust expected values to fit a result. Scope: `ci/publish-interrupt/` and `.github/workflows/qemu-tests.yml` (only if a row must be added). If a fix needs anything else, stop and report.

## Changes

### 1. New test point and case

Add `gc-files-removed` to the list of test points everywhere the nine names are listed (the default-build test, the `run.sh` check that a default build contains none of them, the README). Add case **G3** (garbage-collection disk, test point `gc-files-removed`, firmware both).

### 2. Expected outcomes (replace the T4, G1, G2 rows of the §4 table and add G3)

`p` is 2 on the base disk and 4 on the garbage-collection disk. On the garbage-collection disk, `retain` is 3, so the recovery publish (generation 5) collects every generation left beyond the newest three.

| Case | Boot 1 boots | Record of `p` after boot 1 | Other state after boot 1 |
|---|---|---|---|
| T4, UEFI | `p` (its UKI is in place with `+3-0`) | `GOOD` | bless prints `ACTION: arm_bios generation 2` as a whole console line; `VINTNER_ORDER` starts with `2` and `VINTNER_T_2` is `xxx` after boot 1 (the BIOS side is armed); the old rule 7 report (`generation 2 has BIOS counter ""`) must be **absent** |
| T4, BIOS | os-1 (`p` is not in `VINTNER_ORDER` when boot 1 starts) | `TRYING` (bless armed it after the boot, so it is a trial for boot 2) | bless prints `ACTION: arm_bios generation 2`; `/vintner/bios/os-2/` present; `VINTNER_ORDER == ["2", "1"]`, `VINTNER_T_2 == "xxx"`; the old rule 6 report must be **absent** |
| G1 (`gc-record-removed`) | `p` | `GOOD` | generation 1 has no record, no files (`/vintner/bios/os-1/` and its UKI are gone), is not in `VINTNER_ORDER`, has no `VINTNER_T_1`; no rule 3 report |
| G2 (`gc-grubenv-written`) | `p` | `GOOD` | generation 1 still has its `GOOD` record and its files; it is not in `VINTNER_ORDER` and has no `VINTNER_T_1`; bless prints the whole line `generation 1 has BIOS counter ""` (on both firmwares, since the manifest says `both`) |
| G3 (`gc-files-removed`) | `p` | `GOOD` | generation 1 still has its `GOOD` record; its files are gone; it is not in `VINTNER_ORDER`; bless prints `missing files for generation 1` |

In T4 BIOS, the recovery publish then publishes generation 3 (`next_generation` after boot 1 is 3), and boot 2 boots it. Check that generation 2 is still held and `GOOD` or `TRYING` as the records say, and that no `STAGED` record remains.

For G2 and G3 the recovery publish must remove generation 1 and then generation 2 (the oldest two beyond the newest three, among 1, 2, 3, 4 and 5), and print `vintner publish: removed generation 1` and `vintner publish: removed generation 2` as whole lines, in that order. After it: no files, no record, no `VINTNER_ORDER` entry and no counter for either, and `50vintner-retained-kernels` lists exactly the packages of what remains. For G1, only generation 2 is removed.

### 3. Check adjustments

- **P4:** an image that was `GOOD` before the case stays `GOOD` with its recorded hashes at every read **while it has a record and files**. In G3 generation 1's files are gone by design after the operation boot; accept the missing files for generation 1 only, only in G3, and only when the read after the operation boot and after boot 1 both show the files absent and the record present. Any other missing file is a FAIL. Keep the rule that a vanished record needs the whole `removed generation <g>` line.
- **P5:** replace the T4/G1/G2 expectations with the table above. Every "must be absent" report is a whole-line absence check over bless's output and the publish output.
- **P8:** continues to list reports and actions, now including every `ACTION:` line.
- **Final gate:** `P1…P9 × {C0, T1–T6, M1, M2, G1, G2, G3}`, one row per pair.

### 4. Extra evidence for T4 (read both firmwares)

For T4, print the `grubenv` content (the `VINTNER_*` lines) and the manifest record of generation 2 after the operation boot, after boot 1 and after the recovery publish. This shows exactly when the arming happened.

## Validation before you push

- `bash -n` and `shellcheck -x` on `run.sh`; `python3 -m py_compile` with no `__pycache__` committed.
- In `src/`: `gofmt`, `go vet ./...`, `go test ./...`, and `go test -tags vintner_testpoints ./internal/testpoint/`.
- Synthetic evaluator test (not committed): a correct run gives every row PASS (P8 OBSERVED) on both firmwares; each of these turns its row FAIL: T4 BIOS without the `ACTION: arm_bios generation 2` line, T4 with the rule 6 report still present, G1 with generation 1's files still present, G2 without the `generation 1 has BIOS counter ""` line, G3 with files for generation 1 present, G2 recovery publish without `removed generation 1`, and a duplicated or missing (check, case) row.

## Reporting back

- Both results tables in `ci/publish-interrupt/README.md` and the PR, from that PR's own CI run.
- Any case that differs from the table above, with evidence. Do not adjust expected values.
