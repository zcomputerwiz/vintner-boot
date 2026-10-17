# Candidate fallback integration test (WO-0006)

Tests that a broken candidate image (`TRYING`) falls back to the known-good image (`os-1`) after bounded attempts, on both UEFI and BIOS, across kernel panic, health check failure, hang, and forced power cuts.

```bash
ci/candidate-fallback/run.sh <uefi|bios>
```

CI runs it as `candidate-uefi` and `candidate-bios` in `.github/workflows/qemu-tests.yml`.

## Method

1. **Base disk:** built once per run by `ci/lib/vintner-base.sh`, with `systemd-ukify` and `systemd-boot-efi` installed, the kernel and initramfs hooks copied to `/etc/kernel/postinst.d/zz-vintner` and `/etc/initramfs/post-update.d/zz-vintner`, and `os-1` booted once so that it is blessed `GOOD`. Each case starts from a sparse copy of this disk.
2. **Publish candidate generation 2:** each broken case publishes its candidate, generation 2, through the real publisher:
   - In the operation boot, a modification is placed under `/etc/initramfs-tools/`.
   - `update-initramfs -u -k "$(uname -r)"` runs, triggering the initramfs hook which publishes candidate generation 2 as `TRYING`.
   - In the same operation boot, the modification is removed **without** rebuilding initramfs, ensuring only generation 2 carries the broken behavior while `os-1` remains clean and untouched.
   - The operation script runs with `set -e`, so a failed publish ends the operation with a non-zero `=== OP EXIT ===` and the run stops.
   - The host reads the case disk before the operation boot (os-1's record there is the H6 baseline) and after it. The read after it must show generation 2 `TRYING` with UEFI file `vintner-os-2+3-0.efi` / BIOS `VINTNER_T_2=xxx`, and os-1 `GOOD` with a `last_good` timestamp, UEFI file `vintner-os-1+3-0.efi` / BIOS `VINTNER_T_1=xxx`. Otherwise every row of the case is FAIL, with what the read showed as evidence.
3. **Cases:**
   - **G0 (control):** a harmless file in `/etc/initramfs-tools/conf.d/vintner-test`. Proves the harness itself does not cause a fallback; generation 2 boots normally and is blessed.
   - **B1 (kernel panic):** an executable `init-top` script (`echo c > /proc/sysrq-trigger`). Triggers a kernel panic before the root filesystem is mounted.
   - **B2 (health check failure):** an `init-bottom` script that creates `/run/vintner-test-break`, plus systemd unit `/etc/systemd/system/vintner-test-health.service` (`Type=oneshot`, `ConditionPathExists=/run/vintner-test-break`, `ExecStart=/bin/false`, `Before=boot-complete.target`, `RequiredBy=boot-complete.target`, no `Description=`). Reaches multi-user, but `boot-complete.target` fails, so `vintner-bless.service` never runs. On `os-1`, the condition is false, the unit is skipped, and `boot-complete.target` succeeds.
   - **B3 (hang):** an `init-top` script with `sleep infinity`. Hangs until the 120-second timeout kill.
   - **B4 (early kill / power cut):** a G0-style candidate, but each of its tries is killed by the host 1 second after the kernel's `Command line:` line appears on the serial console.
4. **Boots:** each case is booted 6 times after the operation boot under QEMU, with `-no-reboot` and the serial console logged to a file. A boot ends when the guest powers off, after 120 seconds (timeout kill), or 1 second after `Command line:` (B4). How it ended is recorded from QEMU's exit: `poweroff` (status 0), `qemu exited with status <n>` (any other status not caused by the harness), `killed after 120s timeout`, `killed 1s after Command line`, or for B4 `exited before kill (...)` when the guest had already exited by the time of the kill. A boot without its serial log, run record or host read counts as missing, and every check that needs it fails.
5. **Which image booted:**
   - **Recovery, both firmwares:** a whole token `vintner.recovery=1` or `vintner.recovery_test=1` on the kernel's `Command line:` line marks the boot as recovery (recovery B when `vintner.test_slot=b` is also a token).
   - **UEFI:** the report's `LOADER_ENTRY_SELECTED` where the report ran: the value without its `+N-M` counter and `.efi` must equal `vintner-os-1`, `vintner-os-2`, `vintner-recovery-a` or `vintner-recovery-b`; any other value is kept as the label and fails every check. Where the report did not run (B1, B3, B4 tries, and B2's tries if it does not run there), the boot is the one ordinary generation whose whole UKI file name in `/EFI/Linux/` changed since the previous read (for boot 1, the read after the operation boot); if no generation or more than one changed, the boot is `unknown`.
   - **BIOS:** GRUB's last `vintner: trying <dir>` line on the serial console; `<dir>` must equal `os-1`, `os-2`, `recovery-a` or `recovery-b` (the slot directory `firmware/bios/grub.cfg` prints). No such line means `unknown`.
   - An `unknown` or unexpected label fails every check that needs that boot.
6. **Read on host before and after the operation boot and after every boot, on both firmwares:** the manifest (`vintner manifest show`), the names in `/EFI/Linux/`, `grub-editenv <copy of /grub/grubenv> list` (run from the Debian trixie root `ci/lib/vintner-base.sh` unpacks, as the runner has no GRUB installed), the files under `/vintner/bios/`, and the SHA-256 of every os-1 UKI and every file in `/vintner/bios/os-1/`. Each read is saved under `ci/out/candidate-fallback/<firmware>/` as `<case>-read-<pre|op|bootN>.json` with the raw listings next to it.
7. **Evaluation:** `inspector.py evaluate` computes every row from the saved reads, run records and serial logs after the last case. `run.sh` exits 0 only when `results.tsv` has a PASS row for H1 (G0), H2 to H6 (B1 to B4) and H8, an OBSERVED row for H7 (B1 to B4), and no row with any other status.

## Checks

| ID | Case | Expected | What would make it FAIL |
|---|---|---|---|
| H1 | G0 | Boot 1 is generation 2, blessed: generation 2 `GOOD`, UEFI `vintner-os-2+3-0.efi`, BIOS `VINTNER_T_2=xxx`; every later boot is generation 2 as well; recovery never boots | Any of the 6 boots missing or not identified as os-2 (including `unknown`); after boot 1, generation 2 not `GOOD`, its UEFI file list not exactly `[vintner-os-2+3-0.efi]`, or BIOS `VINTNER_T_2` not `xxx` or `VINTNER_BOOTED` present. |
| H2 | B1 to B4 | Boots 1 to 3 are generation 2. Its counter after each: UEFI `+2-1`, `+1-2`, `+0-3`; BIOS `xx`, `x`, `0` | Any of boots 1 to 3 missing or not identified as os-2; generation 2's UEFI file list after boot 1, 2, 3 not exactly `[vintner-os-2+2-1.efi]`, `[vintner-os-2+1-2.efi]`, `[vintner-os-2+0-3.efi]`, or BIOS `VINTNER_T_2` not `xx`, `x`, `0`. |
| H3 | B1 to B4 | Boot 4 is generation 1 (os-1), not recovery A or B, and is blessed: os-1 `GOOD` with `last_good` later than before the case, UEFI `vintner-os-1+3-0.efi`, BIOS `VINTNER_T_1=xxx` and no `VINTNER_BOOTED` | Boot 4 missing or not identified as os-1; os-1 not `GOOD` after it; os-1's `last_good` before (read after the operation boot) or after missing, unparseable, or the later one not strictly later; UEFI os-1 file list not exactly `[vintner-os-1+3-0.efi]`; BIOS `VINTNER_T_1` not `xxx` or `VINTNER_BOOTED` present. |
| H4 | B1 to B4 | After boot 4: generation 2 is `FAILED` in the manifest, its UEFI file is still `vintner-os-2+0-3.efi` and its BIOS counter still `0`; `vintner bless` printed the transition on the serial console | Boot 4 missing; generation 2 not `FAILED` after it; its UEFI file list not exactly `[vintner-os-2+0-3.efi]` or BIOS `VINTNER_T_2` not `0`; no line of boot 4's serial log (escape codes and `\r` removed) equal to `generation 2: TRYING -> FAILED`. |
| H5 | B1 to B4 | Boots 5 and 6 are os-1 again; generation 2 is never tried again (its counter and name do not change) | Boot 5 or 6 missing or not identified as os-1; after boot 5 or 6, generation 2's UEFI file list, BIOS `VINTNER_T_2` or manifest record differs from after boot 4, or its state is not `FAILED`. |
| H6 | B1 to B4 | os-1's UKI and BIOS files have the hashes its record had before the case, at every read | os-1's record before the operation boot missing or without the half for the firmware under test, or a hash in it not a SHA-256; at the read after the operation boot or after any of the 6 boots: the read missing, not exactly one os-1 UKI, or any file the record lists (`uefi.sha256`, every `bios.files` entry) missing or with a different SHA-256. |
| H7 | B1 to B4 | How boots 1 to 3 ended, and each case's serial evidence for why: B1 the panic line, B2 the failed `vintner-test-health.service` and `boot-complete.target` lines, B3 the last line before the kill, B4 the last line before each kill. OBSERVED | Any of boots 1 to 3 missing, without how it ended, or with an empty serial log; B1: no whole line containing `Kernel panic - not syncing: ` in a boot's log; B2: a boot's log lacks either the `Failed to start vintner-test-health.service` line or the `Dependency failed for boot-complete.target` line; B4: a boot did not end `killed 1s after Command line`. Any of these makes the row FAIL instead of OBSERVED. |
| H8 | all | No boot ran recovery A or B | Any case missing, or any of its 6 boots missing or with a label other than the expected sequence: G0 os-2 six times; B1 to B4 os-2, os-2, os-2, os-1, os-1, os-1. Recovery and `unknown` labels fail. |

## Versions

From CI run [39009077426](https://github.com/zcomputerwiz/vintner-boot/actions/runs/39009077426)'s `versions.txt`:
- **Recovery packages:** `linux-image-6.12.107+deb13-amd64` `6.12.107-1`, `busybox-static` `1:1.37.0-6+b9`, `systemd-boot-efi` `257.13-1~deb13u1`, `systemd-ukify` `257.13-1~deb13u1`, `grub-pc-bin` `2.12-9+deb13u2`, `grub-common` `2.12-9+deb13u2`.
- **Guest (os-1):** kernel `6.12.111+deb13-cloud-amd64` (`linux-image-cloud-amd64` `6.12.111-1`), `systemd` and `udev` `257.13-1~deb13u1`.

## Results

These tables come from the harness before review, which had weaker checks than the one described above; the next CI run replaces them.

Verbatim results from GitHub Actions CI run [39009077426](https://github.com/zcomputerwiz/vintner-boot/actions/runs/39009077426) (commit `8f5bf75`).

### UEFI (`candidate-uefi`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| H1 | G0 | PASS | boot 1 booted os-2; gen 2 state=GOOD; counter=+3-0; boots 2-6 booted os-2 |
| H2 | B1 | PASS | b1=+2-1; b2=+1-2; b3=+0-3 |
| H3 | B1 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:23:49Z (updated); rearmed=+3-0 |
| H4 | B1 | PASS | gen 2 state=FAILED; uefi_file=vintner-os-2+0-3.efi; printed=generation 2: TRYING -> FAILED |
| H5 | B1 | PASS | b5=os-1; b6=os-1; gen2 uefi file remained vintner-os-2+0-3.efi |
| H6 | B1 | PASS | os-1 UKI intact across all boots (sha256=a9001d3cd52cfa64...) |
| H7 | B1 | OBSERVED | B1: killed after 120s timeout; B2: killed after 120s timeout; B3: killed after 120s timeout; b1: Kernel panic - not syncing: sysrq triggered crash; b2: Kernel panic - not syncing: sysrq triggered crash; b3: Kernel panic - not syncing: sysrq triggered crash |
| H2 | B2 | PASS | b1=+2-1; b2=+1-2; b3=+0-3 |
| H3 | B2 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:24:57Z (updated); rearmed=+3-0 |
| H4 | B2 | PASS | gen 2 state=FAILED; uefi_file=vintner-os-2+0-3.efi; printed=generation 2: TRYING -> FAILED |
| H5 | B2 | PASS | b5=os-1; b6=os-1; gen2 uefi file remained vintner-os-2+0-3.efi |
| H6 | B2 | PASS | os-1 UKI intact across all boots (sha256=a9001d3cd52cfa64...) |
| H7 | B2 | OBSERVED | B1: poweroff; B2: poweroff; B3: poweroff; b1: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check.; b2: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check.; b3: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check. |
| H2 | B3 | PASS | b1=+2-1; b2=+1-2; b3=+0-3 |
| H3 | B3 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:31:43Z (updated); rearmed=+3-0 |
| H4 | B3 | PASS | gen 2 state=FAILED; uefi_file=vintner-os-2+0-3.efi; printed=generation 2: TRYING -> FAILED |
| H5 | B3 | PASS | b5=os-1; b6=os-1; gen2 uefi file remained vintner-os-2+0-3.efi |
| H6 | B3 | PASS | os-1 UKI intact across all boots (sha256=a9001d3cd52cfa64...) |
| H7 | B3 | OBSERVED | B1: killed after 120s timeout; B2: killed after 120s timeout; B3: killed after 120s timeout; b1 last_line: Loading, please wait...; b2 last_line: Loading, please wait...; b3 last_line: Loading, please wait... |
| H2 | B4 | PASS | b1=+2-1; b2=+1-2; b3=+0-3 |
| H3 | B4 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:32:35Z (updated); rearmed=+3-0 |
| H4 | B4 | PASS | gen 2 state=FAILED; uefi_file=vintner-os-2+0-3.efi; printed=generation 2: TRYING -> FAILED |
| H5 | B4 | PASS | b5=os-1; b6=os-1; gen2 uefi file remained vintner-os-2+0-3.efi |
| H6 | B4 | PASS | os-1 UKI intact across all boots (sha256=a9001d3cd52cfa64...) |
| H7 | B4 | OBSERVED | B1: killed 1s after Command line; B2: killed 1s after Command line; B3: killed 1s after Command line; b1 last_line: [    1.056266] Block layer SCSI generic (bsg) driver version 0.4 loaded (major 247); b2 last_line: [    1.033684] Asymmetric key parser 'x509' registered; b3 last_line: [    1.074731] Loading compiled-in X.509 certificates |
| H8 | all | PASS | no boot ran recovery A or B across all cases |

### BIOS (`candidate-bios`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| H1 | G0 | PASS | boot 1 booted os-2; gen 2 state=GOOD; counter=xxx; booted_unset=True; boots 2-6 booted os-2 |
| H2 | B1 | PASS | b1=xx; b2=x; b3=0 |
| H3 | B1 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:22:42Z (updated); rearmed=xxx; booted_unset=True |
| H4 | B1 | PASS | gen 2 state=FAILED; bios_t=0; printed=generation 2: TRYING -> FAILED |
| H5 | B1 | PASS | b5=os-1; b6=os-1; gen2 counter remained 0 |
| H6 | B1 | PASS | os-1 BIOS files intact across all boots |
| H7 | B1 | OBSERVED | B1: killed after 120s timeout; B2: killed after 120s timeout; B3: killed after 120s timeout; b1: Kernel panic - not syncing: sysrq triggered crash; b2: Kernel panic - not syncing: sysrq triggered crash; b3: Kernel panic - not syncing: sysrq triggered crash |
| H2 | B2 | PASS | b1=xx; b2=x; b3=0 |
| H3 | B2 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:23:38Z (updated); rearmed=xxx; booted_unset=True |
| H4 | B2 | PASS | gen 2 state=FAILED; bios_t=0; printed=generation 2: TRYING -> FAILED |
| H5 | B2 | PASS | b5=os-1; b6=os-1; gen2 counter remained 0 |
| H6 | B2 | PASS | os-1 BIOS files intact across all boots |
| H7 | B2 | OBSERVED | B1: poweroff; B2: poweroff; B3: poweroff; b1: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check.; b2: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check.; b3: [FAILED] Failed to start vintner-test-health.service.; [DEPEND] Dependency failed for boot-complete.target - Boot Completion Check. |
| H2 | B3 | PASS | b1=xx; b2=x; b3=0 |
| H3 | B3 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:30:18Z (updated); rearmed=xxx; booted_unset=True |
| H4 | B3 | PASS | gen 2 state=FAILED; bios_t=0; printed=generation 2: TRYING -> FAILED |
| H5 | B3 | PASS | b5=os-1; b6=os-1; gen2 counter remained 0 |
| H6 | B3 | PASS | os-1 BIOS files intact across all boots |
| H7 | B3 | OBSERVED | B1: killed after 120s timeout; B2: killed after 120s timeout; B3: killed after 120s timeout; b1 last_line: Loading, please wait...; b2 last_line: Loading, please wait...; b3 last_line: Loading, please wait... |
| H2 | B4 | PASS | b1=xx; b2=x; b3=0 |
| H3 | B4 | PASS | boot 4 booted os-1; os-1 state=GOOD; last_good=2026-10-17T06:31:03Z (updated); rearmed=xxx; booted_unset=True |
| H4 | B4 | PASS | gen 2 state=FAILED; bios_t=0; printed=generation 2: TRYING -> FAILED |
| H5 | B4 | PASS | b5=os-1; b6=os-1; gen2 counter remained 0 |
| H6 | B4 | PASS | os-1 BIOS files intact across all boots |
| H7 | B4 | OBSERVED | B1: killed 1s after Command line; B2: killed 1s after Command line; B3: killed 1s after Command line; b1 last_line: [    0.963434] ata6: SATA max UDMA/133 abar m4096@0xfeb82000 port 0xfeb82380 irq 30 lpm-pol 1; b2 last_line: [    0.939090] ata6: SATA max UDMA/133 abar m4096@0xfeb82000 port 0xfeb82380 irq 30 lpm-pol 1; b3 last_line: [    0.995283] ata6: SATA max UDMA/133 abar m4096@0xfeb82000 port 0xfeb82380 irq 30 lpm-pol 1 |
| H8 | all | PASS | no boot ran recovery A or B across all cases |

## Findings

1. **B1 (Kernel Panic) behavior:**
   When the kernel panics via SysRq-c in the initramfs (`init-top`), the kernel halts execution after printing `---[ end Kernel panic - not syncing: sysrq triggered crash ]---` and does not power off or reboot (no `panic=` reboot timeout is set on the kernel command line). The VM remains halted until terminated by the 120-second timeout.
2. **klibc utilities in Debian initramfs:**
   - Debian's initramfs uses `klibc-utils`. `touch` does not exist in klibc, so files must be created via shell redirection (`: > /path/to/file`).
   - klibc's `sleep` command only parses decimal numbers (`seconds[.fraction]`) and exits with error code 1 on non-numeric inputs like `infinity`. An infinite delay in initramfs requires a shell loop (`while :; do sleep infinity 2>/dev/null || sleep 86400; done`).
3. **Serial console logging from systemd units:**
   By default the output of a `Type=oneshot` unit goes to the journal only, so the transition `generation 2: TRYING -> FAILED` that `vintner bless` prints would not reach the serial console that H4 reads. `integration/systemd-units/vintner-bless.service` therefore sets `StandardOutput=journal+console`: systemd sends each line bless prints on stdout to the console as well, as its own line. `vintner bless` itself only prints to stdout and does not open `/dev/console` or any other terminal.
