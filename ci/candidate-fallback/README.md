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
3. **Cases:**
   - **G0 (control):** a harmless file in `/etc/initramfs-tools/conf.d/vintner-test`. Proves the harness itself does not cause a fallback; generation 2 boots normally and is blessed.
   - **B1 (kernel panic):** an executable `init-top` script (`echo c > /proc/sysrq-trigger`). Triggers a kernel panic before the root filesystem is mounted.
   - **B2 (health check failure):** an `init-bottom` script that creates `/run/vintner-test-break`, plus systemd unit `/etc/systemd/system/vintner-test-health.service` (`Type=oneshot`, `ConditionPathExists=/run/vintner-test-break`, `ExecStart=/bin/false`, `Before=boot-complete.target`, `RequiredBy=boot-complete.target`, no `Description=`). Reaches multi-user, but `boot-complete.target` fails, so `vintner-bless.service` never runs. On `os-1`, the condition is false, the unit is skipped, and `boot-complete.target` succeeds.
   - **B3 (hang):** an `init-top` script with `sleep infinity`. Hangs until the 120-second timeout kill.
   - **B4 (early kill / power cut):** a G0-style candidate, but each of its tries is killed by the host 1 second after the kernel's `Command line:` line appears on the serial console.
4. **Boots:** each case is booted up to 6 times after the operation boot under QEMU, with `-no-reboot` and the serial console logged to a file. A boot ends when the guest powers off, after 120 seconds (timeout kill), or 1 second after `Command line:` (B4).
5. **Which image booted:**
   - **UEFI:** the report's `LOADER_ENTRY_SELECTED` where the report ran. Where the report did not run (B1, B3, B4), identified by the kernel's `Command line:` and by inspecting which UKI entry in `/EFI/Linux/` was renamed by `systemd-boot` (the boot counter decremented from the previous boot).
   - **BIOS:** GRUB's last `vintner: trying <gen>` line printed to the serial console, confirmed by the decremented counter in `grubenv`.
6. **Read on host after every boot:** the manifest, entries in `/EFI/Linux/`, `grubenv`, and `/vintner/bios/` slot files and hashes.

## Checks

| ID | Case | Expected | What would make it FAIL |
|---|---|---|---|
| H1 | G0 | Boot 1 is generation 2, blessed: generation 2 `GOOD`, UEFI `vintner-os-2+3-0.efi`, BIOS `VINTNER_T_2=xxx`; every later boot is generation 2 as well; recovery never boots | Any boot running os-1 or recovery; generation 2 not blessed GOOD or counter not re-armed to +3-0 / xxx; any later boot not running generation 2. |
| H2 | B1 to B4 | Boots 1 to 3 are generation 2. Its counter after each: UEFI `+2-1`, `+1-2`, `+0-3`; BIOS `xx`, `x`, `0` | Any of boots 1 to 3 running os-1 or recovery; any counter differing from the expected decrement progression after each try. |
| H3 | B1 to B4 | Boot 4 is generation 1 (os-1), not recovery A or B, and is blessed: os-1 `GOOD` with `last_good` later than before the case, UEFI `vintner-os-1+3-0.efi`, BIOS `VINTNER_T_1=xxx` and no `VINTNER_BOOTED` | Boot 4 running generation 2 or recovery A or B; os-1 not blessed GOOD; `last_good` timestamp not updated; counter not re-armed (+3-0 / xxx); `VINTNER_BOOTED` left set in grubenv. |
| H4 | B1 to B4 | After boot 4: generation 2 is `FAILED` in the manifest, its UEFI file is still `vintner-os-2+0-3.efi` and its BIOS counter still `0`; `vintner bless` printed the transition on the serial console | Generation 2 state not FAILED in manifest; generation 2 file missing or renamed from +0-3 / counter not 0; `generation 2: TRYING -> FAILED` not printed on serial console. |
| H5 | B1 to B4 | Boots 5 and 6 are os-1 again; generation 2 is never tried again (its counter and name do not change) | Boot 5 or 6 running generation 2 or recovery; generation 2 counter decremented or filename modified. |
| H6 | B1 to B4 | os-1's UKI and BIOS files have the hashes its record had before the case, at every read | Any mismatch between os-1's files / UKI SHA-256 and the initial pre-case hashes at any of the 6 reads. |
| H7 | B1 to B4 | How boots 1 to 3 ended, and each case's serial evidence for why: B1 the panic line, B2 the failed `vintner-test-health.service` and `boot-complete.target` lines, B3 the last line before the kill, B4 the last line before each kill. OBSERVED | Missing evidence or missing boot logs. |
| H8 | all | No boot ran recovery A or B | Any boot in any case running recovery A or recovery B. |

## Versions

From CI run [39009077426](https://github.com/zcomputerwiz/vintner-boot/actions/runs/39009077426)'s `versions.txt`:
- **Recovery packages:** `linux-image-6.12.107+deb13-amd64` `6.12.107-1`, `busybox-static` `1:1.37.0-6+b9`, `systemd-boot-efi` `257.13-1~deb13u1`, `systemd-ukify` `257.13-1~deb13u1`, `grub-pc-bin` `2.12-9+deb13u2`, `grub-common` `2.12-9+deb13u2`.
- **Guest (os-1):** kernel `6.12.111+deb13-cloud-amd64` (`linux-image-cloud-amd64` `6.12.111-1`), `systemd` and `udev` `257.13-1~deb13u1`.

## Results

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
   Because `vintner-bless.service` has `Type=oneshot` and no `StandardOutput=console`, messages logged by `vintner bless` to stdout/stderr are captured by systemd-journald and do not reach `/dev/console` / `ttyS0` by default. To ensure the transition `generation 2: TRYING -> FAILED` appears in the serial console log for verification (Check H4), `vintner bless` writes directly to `/dev/console` (falling back to `/dev/ttyS0` if needed) when running as root.
