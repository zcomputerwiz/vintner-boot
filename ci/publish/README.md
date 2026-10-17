# The publisher: kernel and initramfs updates become new boot images (WO-0005)

Tests that kernel upgrade and initramfs-only regeneration trigger the publisher (`vintner publish`) and create bootable candidates, preserving known-good images, under real APT operations across UEFI and BIOS (architecture §6, §9 phase 2).

```
ci/publish/run.sh <uefi|bios>
```

CI runs it as `publish-uefi` and `publish-bios` in `.github/workflows/qemu-tests.yml`.

## Method

1. **Base disk:** built once per run by `ci/lib/vintner-base.sh`, with dual firmware (`both`).
2. **Prepared disk:** from the base disk:
   - installs `systemd-ukify` and `systemd-boot-efi` in the guest with `apt-get` (not `systemd-boot`, which is a conflicting package, architecture §5);
   - copies the two hooks `integration/kernel-hooks/zz-vintner` and `integration/initramfs-hooks/zz-vintner` (`chmod 0755`) and the host-built static `vintner` binary to the guest root;
   - boots once so `os-1` is blessed `GOOD`.
   - verified before any cases that the prepared disk has exactly one image, `os-1`, `GOOD`, and nothing under `/vintner/staging/`.
3. **Cases:** each case starts from a fresh sparse copy of the prepared disk.

   | ID | Guest operation | Then |
   |---|---|---|
   | C0 | `true` (control) | reboot once |
   | K1 | `apt-get -y install linux-image-amd64`, a second kernel with a new ABI | reboot once |
   | K2 | add a line to `/etc/initramfs-tools/conf.d/vintner-test`, then `update-initramfs -u -k "$(uname -r)"` | reboot once |
   | K3 | K2 twice in one operation, then `vintner publish --kernel "$(uname -r)"` | reboot once |
   | U1 | wait for `vintner-bless.service` to finish, list every file on boot storage with its SHA-256, run `vintner publish --kernel "$(uname -r)"` with nothing changed, list again | none |
   | G1 | K2 then reboot, four times in a row | — |
   | R1 | K1, then `apt-get -y purge` the new kernel's package (generation 2's `kernel.package`), then K2, all in one operation, still running os-1's kernel | reboot once |

4. **Boots:** under KVM with `-no-reboot` and the serial console logged to file. A boot completes when `vintner-lifecycle-report.service` finishes and powers the guest off.
5. **Which image booted:**
   - UEFI: report's `LOADER_ENTRY_SELECTED` (or `LoaderBootCountPath`).
   - BIOS: GRUB's last `vintner: trying <gen>` line on serial console.
6. **Host inspections after boots:**
   - Manifest: read from both copies on boot storage using host-built `vintner manifest show`.
   - Files on boot storage: `/EFI/Linux/` UKIs, `/vintner/bios/` slot files, `/vintner/staging/`.
   - Variables in `grubenv`: parsed with exact matching on `VINTNER_ORDER` and `VINTNER_T_<gen>`.
   - Guest root: `/etc/apt/apt.conf.d/50vintner-retained-kernels` and `/lib/modules/<abi>` module tree.

## Checks

| ID | Case | Expected / PASS when | Fails on |
|---|---|---|---|
| E1 | C0 | No image added: `next_generation` and the generations in `images` unchanged; no file added under `/EFI/Linux/`, `/vintner/bios/` or `/vintner/staging/`; `VINTNER_ORDER` unchanged; os-1 booted | any image or file added, staging non-empty, order changed, or wrong image booted |
| E2 | K1, K2 | Exactly one new image, generation 2:<br>- state `TRYING`; `kernel.abi`, `kernel.package` and `module_tree` matching the guest's files;<br>- UEFI file `vintner-os-2+3-0.efi`;<br>- BIOS directory `os-2` whose three files match the manifest hashes;<br>- `VINTNER_ORDER=2 1` and `VINTNER_T_2=xxx`;<br>- `/vintner/staging/` empty;<br>- `NeverAutoRemove` listing exactly the packages of images 1 and 2 | state not TRYING, missing files or hash mismatch, order or counter wrong, staging non-empty, or package list mismatch |
| E3 | K1, K2 | The next boot ran generation 2, and after it the manifest has image 2 `GOOD` with `first_good` set | generation 2 did not boot, state not GOOD, or `first_good` not set |
| E4 | K1, K2 | os-1 preserved: its UKI and BIOS files have the hashes its record had before the operation; its record is unchanged apart from `last_good`, which the operation's own boot updates; `state` is still `GOOD` and `stale` still `false`; it is still in `VINTNER_ORDER` with `VINTNER_T_1=xxx` and in `/EFI/Linux/` as `vintner-os-1+3-0.efi` | os-1 files modified or missing, state not GOOD, stale not false, or dropped from order/counter |
| E5 | K3 | Exactly two new generations, 2 and 3, one per changed initramfs. The extra `vintner publish` printed "unchanged" and added none | fewer or more generations added, next_generation not 4, or unchanged not printed |
| E6 | U1 | Exit 0, "unchanged" printed, and the two listings identical | exit non-zero, unchanged not printed, or any boot storage file added/modified/removed |
| E7 | G1 | After each round, the images kept are exactly those `schema/state-machine.md` "Garbage collection" keeps with `retain` 3. Every removed image's record, UKI, BIOS directory, `VINTNER_ORDER` entry and counter are gone. The `NeverAutoRemove` file lists exactly the kept images' packages | kept images do not match retain rules, removed image files or counters persist, or package list mismatch |
| E8 | R1 | Generation 2 is still on boot storage and in the manifest after the operation, and `stale: true`. Generation 3 (K2's) is `TRYING` and os-1 is `GOOD`. The reboot ran generation 3 | gen 2 not stale or missing from boot storage, gen 3 not TRYING, os-1 not GOOD, or reboot did not run gen 3 |
| E9 | all | Every boot ran an ordinary image; none fell back to recovery. Report how each boot ended | any boot ran recovery or failed to boot |

The script exits 0 when every row passes.

## Versions

From the run's `versions.txt`:
- Host: `systemd-boot-efi` and `systemd-ukify` `257.13-1~deb13u1`; `grub-pc-bin` and `grub-common` `2.12-9+deb13u2`.
- Guest (os-1): kernel `6.12.111+deb13-cloud-amd64` (`linux-image-cloud-amd64` `6.12.111-1`), `systemd` and `udev` `257.13-1~deb13u1`.
- Guest (second kernel): `linux-image-6.12.111+deb13-amd64` (`linux-image-amd64` `6.12.111-1`).

## Results

<!-- Results tables will be copied verbatim from the CI run -->
