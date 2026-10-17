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

From the run's `versions.txt`:

(To be populated from CI run)

## Results

(To be populated with verbatim results from CI run)
