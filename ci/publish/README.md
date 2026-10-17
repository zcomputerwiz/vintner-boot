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
   - in the same operation, holds `linux-image-cloud-amd64` and os-1's kernel package `linux-image-$KERNEL_ABI` (ABI from the base disk's `base.env`) with `apt-mark hold`. This keeps any upgrade of the cloud kernel from publishing an extra generation during the cases;
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

   K3 and R1 run under `set -e`, so a failed step ends the operation with a non-zero exit. R1 picks the package to purge as the `dpkg-query -S` owner of `/boot/vmlinuz-<abi>`, where `<abi>` is the one installed ABI that is not `uname -r`.

4. **Boots:** under KVM with `-no-reboot` and the serial console logged to file. A boot completes when `vintner-lifecycle-report.service` finishes and powers the guest off.
5. **Which image booted:**
   - UEFI: report's `LOADER_ENTRY_SELECTED` (or `LoaderBootCountPath`). The whole entry name must be `vintner-os-<g>.efi`, with or without a `+<left>[-<done>]` counter, for the boot to count as `os-<g>`.
   - BIOS: GRUB's last `vintner: trying <gen>` line on serial console.
6. **What the harness reads, and when:**
   - **When:**
     - the prepared disk, after the second preparation boot. It is also the "before the operation" reference for every case: os-1's record, ABI and package, the `/EFI/Linux/` and `/vintner/bios/` lists, and `grubenv`;
     - C0 and K3 after the reboot; U1 after its only boot;
     - K1, K2 and R1 twice: on a copy of the disk taken after the operation boot (E2, E8, and E4's record comparison), and after the reboot (E3, E4);
     - G1 after the reboot of each of the four rounds (E7, one row per round);
     - the guest root, mounted read-only on the host: K1 and K2 after the reboot, G1 after each round's reboot.

     Every other boot is checked only through its serial log (E9).
   - **Boot storage**, read with mtools; any mtools error other than "not found" stops the run:
     - the manifest, both copies, through the host-built `vintner manifest show`;
     - the names under `/EFI/Linux/` (files), `/vintner/bios/` (files and directories) and `/vintner/staging/` (files and directories), and the contents of os-1's and generation 2's UKI and BIOS files for their hashes;
     - `/grub/grubenv`, parsed by the inspector rather than `grub-editenv list`. A missing file, or one that does not start with the GRUB environment header, fails every check that reads it. `VINTNER_T_<g>` is compared whole. `VINTNER_ORDER` is compared whole in E1 (with the prepared disk's) and E2 (`2 1`); E4 and E7 check only whether a generation is one of its space-separated entries.
   - **Guest root:** every quoted string inside `APT::NeverAutoRemove { ... };` in `/etc/apt/apt.conf.d/50vintner-retained-kernels`, kept raw, with escapes; `/boot/vmlinuz-*`; the module-tree inputs (`/boot/vmlinuz-<abi>`, `/lib/modules/<abi>/modules.dep`, `/lib/modules/<abi>/updates/dkms`); `/var/lib/dpkg/info/*.list` and `/var/lib/dpkg/status`.
   - **Serial logs** of every boot: the report's `LOADER_ENTRY_SELECTED`, `BLESS_ACTIVE` and `=== VINTNER-REPORT-END ===`, GRUB's `vintner: trying` lines, and `=== OP EXIT: <n> ===`. K3 and U1 print the explicit `vintner publish`'s stdout between `=== PUBLISH-BEGIN ===` and `=== PUBLISH-END ===`, then `PUBLISH_EXIT=<n>`. U1 prints its two listings (`sha256sum` of every file on boot storage) between `=== LISTING-BEFORE-BEGIN/END ===` and `=== LISTING-AFTER-BEGIN/END ===`, or a failed-mount line. R1 prints `PURGED_PACKAGE=<package>`.

## Checks

| ID | Case | Expected / PASS when | Fails on |
|---|---|---|---|
| E1 | C0 | No image added: `next_generation` and the generations in `images` unchanged; no file added under `/EFI/Linux/`, `/vintner/bios/` or `/vintner/staging/`; `VINTNER_ORDER` unchanged; os-1 booted | `next_generation` or the generations differ from the prepared disk's; the `/EFI/Linux/` or `/vintner/bios/` file list differs; any entry, file or directory, under `/vintner/staging/`; `VINTNER_ORDER` missing or not equal to the prepared disk's; `grubenv` missing or unreadable; the reboot did not run os-1 |
| E2 | K1, K2 | Exactly one new image, generation 2:<br>- state `TRYING`; `kernel.abi`, `kernel.package` and `module_tree` matching the guest's files;<br>- UEFI file `vintner-os-2+3-0.efi`;<br>- BIOS directory `os-2` whose three files match the manifest hashes;<br>- `VINTNER_ORDER=2 1` and `VINTNER_T_2=xxx`;<br>- `/vintner/staging/` empty;<br>- `NeverAutoRemove` listing exactly the packages of images 1 and 2 | Read after the operation boot. Generations not exactly `[1, 2]` or `next_generation` not 3; image 2 not `TRYING`; `kernel.abi` not the guest's one `/boot/vmlinuz-*` ABI other than os-1's (K1) or not os-1's ABI (K2); `kernel.package` not the one package whose dpkg file list has the line `/boot/vmlinuz-<abi>`, or `kernel.version` not that package's `Version` in the guest's dpkg status; `module_tree` not the one computed from the guest root, or its inputs missing; `vintner-os-2+3-0.efi` or a BIOS file missing or not matching the manifest hash; the whole `/EFI/Linux/` list not os-1's and the recovery UKI names from before the operation plus `vintner-os-2+3-0.efi`; the whole `/vintner/bios/` list not the entries from before plus `os-2/` with `vmlinuz`, `initrd.img` and `slot.cfg`; `VINTNER_ORDER` not exactly `2 1` or `VINTNER_T_2` not exactly `xxx`; any entry under `/vintner/staging/`; the raw `NeverAutoRemove` strings not exactly `^<package>$`, escaped as Go's `regexp.QuoteMeta` does, for os-1's package from before the operation and the owner found above; the apt file missing or unparseable; `grubenv` missing or unreadable; guest root not mounted |
| E3 | K1, K2 | The next boot ran generation 2, and after it the manifest has image 2 `GOOD` with `first_good` set | generation 2 did not boot, state not GOOD, or `first_good` not set |
| E4 | K1, K2 | os-1 preserved: its UKI and BIOS files have the hashes its record had before the operation; its record is unchanged apart from `last_good`, which the operation's own boot updates; `state` is still `GOOD` and `stale` still `false`; it is still in `VINTNER_ORDER` with `VINTNER_T_1=xxx` and in `/EFI/Linux/` as `vintner-os-1+3-0.efi` | os-1's record after the operation boot or after the reboot differs from the prepared disk's in any key but `last_good`; `state` not `GOOD` or `stale` not `false`; `vintner-os-1+3-0.efi` or an os-1 BIOS file missing or not matching the hashes recorded before the operation; `1` not an entry of `VINTNER_ORDER`; `VINTNER_T_1` not exactly `xxx`; `grubenv` missing or unreadable |
| E5 | K3 | Exactly two new generations, 2 and 3, one per changed initramfs. The extra `vintner publish` printed "unchanged" and added none | generations not exactly `[1, 2, 3]` or `next_generation` not 4; no line exactly `unchanged` between the publish markers, or `PUBLISH_EXIT` not `0` |
| E6 | U1 | Exit 0, "unchanged" printed, and the two listings identical | `PUBLISH_EXIT` not `0`; no line exactly `unchanged` between the publish markers; either listing empty, or the two not identical as whole lists; the operation exit not `0` (it is 1 when boot storage does not mount) |
| E7 | G1 (rows `G1r1` to `G1r4`, one per round) | After each round, the images kept are exactly those `schema/state-machine.md` "Garbage collection" keeps with `retain` 3. Every removed image's record, UKI, BIOS directory, `VINTNER_ORDER` entry and counter are gone. The `NeverAutoRemove` file lists exactly the kept images' packages | Generations not exactly `[1, 2]`, `[1, 2, 3]`, `[2, 3, 4]`, `[3, 4, 5]` after rounds 1 to 4 (retain 3, os-1 `GOOD`, each new image blessed `GOOD` by its reboot); a removed generation `<g>` still has its record, any `/EFI/Linux/vintner-os-<g>+*.efi` or `vintner-os-<g>.efi`, anything under `/vintner/bios/os-<g>/`, a `VINTNER_ORDER` entry or `VINTNER_T_<g>`; the whole `/EFI/Linux/` or `/vintner/bios/` list not the prepared disk's without the removed generations, plus `vintner-os-<g>+3-0.efi` and `os-<g>/` with its three files for each new kept generation; the raw `NeverAutoRemove` strings not exactly os-1's package, which every G1 image republishes; `grubenv` or `VINTNER_ORDER` missing; guest root not mounted |
| E8 | R1 | Generation 2 is still on boot storage and in the manifest after the operation, and `stale: true`. Generation 3 (K2's) is `TRYING` and os-1 is `GOOD`. The reboot ran generation 3 | Read after the operation boot. Gen 2 missing, not `stale: true`, its UKI or `slot.cfg` missing from boot storage, or its `kernel.package` not the `PURGED_PACKAGE` the guest printed; gen 3 not `TRYING` or its `kernel.abi` not os-1's; os-1 not `GOOD`; the reboot did not run gen 3 |
| E9 | all | Every boot ran an ordinary image; none fell back to recovery. Report how each boot ended | any boot, the two preparation boots included, did not run exactly `os-<n>`, or its log has no `=== VINTNER-REPORT-END ===` or no line `BLESS_ACTIVE: active`; the operation boot of K1, K2, K3, each G1 round or R1 did not print `=== OP EXIT: 0 ===`. The evidence lists every boot with its image, whether the report end was seen, the bless state and, for operation boots, the exit code |

The script exits 0 only when `results.tsv` has a PASS row for each of E1 C0, E2 to E4 for K1 and K2, E5 K3, E6 U1, E7 `G1r1` to `G1r4`, E8 R1 and E9 all, and no FAIL row. A missing row fails the run.

## Versions

From the run's `versions.txt`:
- Host: `systemd-boot-efi` and `systemd-ukify` `257.13-1~deb13u1`; `grub-pc-bin` and `grub-common` `2.12-9+deb13u2`.
- Guest (os-1): kernel `6.12.111+deb13-cloud-amd64` (`linux-image-cloud-amd64` `6.12.111-1`), `systemd` and `udev` `257.13-1~deb13u1`.
- Guest (second kernel): `linux-image-6.12.113+deb13-amd64` (`linux-image-amd64` `6.12.113-1`).

## Results

GitHub Actions, KVM, 2026-10-17: run 39006294637 on commit `2df2420`. Copied from each job's `results.tsv`.

### UEFI (`publish-uefi`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| E1 | C0 | PASS | next_gen=2; images=[1]; EFI/Linux unchanged (3 files); vintner/bios unchanged (9 entries); staging empty; VINTNER_ORDER=1; booted=os-1 |
| E2 | K1 | PASS | generations=[1, 2] next_generation=3; guest /boot/vmlinuz-* other than os-1's 6.12.111+deb13-cloud-amd64: ['6.12.113+deb13-amd64']; guest: owners of /boot/vmlinuz-6.12.113+deb13-amd64=['linux-image-6.12.113+deb13-amd64'] version=6.12.113-1; state=TRYING; abi=6.12.113+deb13-amd64 pkg=linux-image-6.12.113+deb13-amd64 version=6.12.113-1; module_tree matches guest; UEFI UKI verified (vintner-os-2+3-0.efi); BIOS files verified (3/3); EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (16 entries); VINTNER_ORDER=2 1; VINTNER_T_2=xxx; staging empty; NeverAutoRemove verified: ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$', '^linux-image-6\\.12\\.113\\+deb13-amd64$'] |
| E3 | K1 | PASS | next boot ran os-2; image 2 GOOD (first_good=2026-10-17T05:31:44Z) |
| E4 | K1 | PASS | os-1 state=GOOD stale=false; os-1 record after op unchanged apart from last_good; os-1 record after reboot unchanged apart from last_good; os-1 UKI file intact (vintner-os-1+3-0.efi); os-1 BIOS files intact; os-1 in VINTNER_ORDER (2 1); VINTNER_T_1=xxx |
| E2 | K2 | PASS | generations=[1, 2] next_generation=3; guest: owners of /boot/vmlinuz-6.12.111+deb13-cloud-amd64=['linux-image-6.12.111+deb13-cloud-amd64'] version=6.12.111-1; state=TRYING; abi=6.12.111+deb13-cloud-amd64 pkg=linux-image-6.12.111+deb13-cloud-amd64 version=6.12.111-1; module_tree matches guest; UEFI UKI verified (vintner-os-2+3-0.efi); BIOS files verified (3/3); EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (16 entries); VINTNER_ORDER=2 1; VINTNER_T_2=xxx; staging empty; NeverAutoRemove verified: ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E3 | K2 | PASS | next boot ran os-2; image 2 GOOD (first_good=2026-10-17T05:32:14Z) |
| E4 | K2 | PASS | os-1 state=GOOD stale=false; os-1 record after op unchanged apart from last_good; os-1 record after reboot unchanged apart from last_good; os-1 UKI file intact (vintner-os-1+3-0.efi); os-1 BIOS files intact; os-1 in VINTNER_ORDER (2 1); VINTNER_T_1=xxx |
| E5 | K3 | PASS | exactly generations [1, 2, 3] present; next_generation=4; extra publish: PUBLISH_EXIT=0, stdout=['unchanged', 'vintner publish: updated apt retained kernels'] |
| E6 | U1 | PASS | op exit=0; PUBLISH_EXIT=0; publish stdout=['unchanged', 'vintner publish: updated apt retained kernels']; listings identical (16 files) |
| E7 | G1r1 | PASS | kept generations=[1, 2]; VINTNER_ORDER=2 1; EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (16 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E7 | G1r2 | PASS | kept generations=[1, 2, 3]; VINTNER_ORDER=3 2 1; EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-os-3+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (20 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E7 | G1r3 | PASS | kept generations=[2, 3, 4]; gen 1 record/UKI/BIOS dir/order entry/counter gone; VINTNER_ORDER=4 3 2; EFI/Linux=['/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-os-3+3-0.efi', '/EFI/Linux/vintner-os-4+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (20 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E7 | G1r4 | PASS | kept generations=[3, 4, 5]; gen 1 record/UKI/BIOS dir/order entry/counter gone; gen 2 record/UKI/BIOS dir/order entry/counter gone; VINTNER_ORDER=5 4 3; EFI/Linux=['/EFI/Linux/vintner-os-3+3-0.efi', '/EFI/Linux/vintner-os-4+3-0.efi', '/EFI/Linux/vintner-os-5+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (20 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E8 | R1 | PASS | gen 2 stale=true; gen 2 kernel.package=linux-image-6.12.113+deb13-amd64 = purged package; gen 2 files present on boot storage; gen 3 state=TRYING; gen 3 kernel.abi=6.12.111+deb13-cloud-amd64; os-1 state=GOOD; reboot ran os-3 |
| E9 | all | PASS | prep(B1):os-1,report end seen,bless active; prep(B2):os-1,report end seen,bless active; C0(B1):os-1,report end seen,bless active; C0(B2):os-1,report end seen,bless active; K1(B1):os-1,report end seen,bless active,op exit 0; K1(B2):os-2,report end seen,bless active; K2(B1):os-1,report end seen,bless active,op exit 0; K2(B2):os-2,report end seen,bless active; K3(B1):os-1,report end seen,bless active,op exit 0; K3(B2):os-3,report end seen,bless active; U1(B1):os-1,report end seen,bless active; G1(BR1op):os-1,report end seen,bless active,op exit 0; G1(BR1rb):os-2,report end seen,bless active; G1(BR2op):os-2,report end seen,bless active,op exit 0; G1(BR2rb):os-3,report end seen,bless active; G1(BR3op):os-3,report end seen,bless active,op exit 0; G1(BR3rb):os-4,report end seen,bless active; G1(BR4op):os-4,report end seen,bless active,op exit 0; G1(BR4rb):os-5,report end seen,bless active; R1(B1):os-1,report end seen,bless active,op exit 0; R1(B2):os-3,report end seen,bless active |

### BIOS (`publish-bios`)

| ID | Case | Status | Evidence |
|---|---|---|---|
| E1 | C0 | PASS | next_gen=2; images=[1]; EFI/Linux unchanged (3 files); vintner/bios unchanged (9 entries); staging empty; VINTNER_ORDER=1; booted=os-1 |
| E2 | K1 | PASS | generations=[1, 2] next_generation=3; guest /boot/vmlinuz-* other than os-1's 6.12.111+deb13-cloud-amd64: ['6.12.113+deb13-amd64']; guest: owners of /boot/vmlinuz-6.12.113+deb13-amd64=['linux-image-6.12.113+deb13-amd64'] version=6.12.113-1; state=TRYING; abi=6.12.113+deb13-amd64 pkg=linux-image-6.12.113+deb13-amd64 version=6.12.113-1; module_tree matches guest; UEFI UKI verified (vintner-os-2+3-0.efi); BIOS files verified (3/3); EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (16 entries); VINTNER_ORDER=2 1; VINTNER_T_2=xxx; staging empty; NeverAutoRemove verified: ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$', '^linux-image-6\\.12\\.113\\+deb13-amd64$'] |
| E3 | K1 | PASS | next boot ran os-2; image 2 GOOD (first_good=2026-10-17T05:30:25Z) |
| E4 | K1 | PASS | os-1 state=GOOD stale=false; os-1 record after op unchanged apart from last_good; os-1 record after reboot unchanged apart from last_good; os-1 UKI file intact (vintner-os-1+3-0.efi); os-1 BIOS files intact; os-1 in VINTNER_ORDER (2 1); VINTNER_T_1=xxx |
| E2 | K2 | PASS | generations=[1, 2] next_generation=3; guest: owners of /boot/vmlinuz-6.12.111+deb13-cloud-amd64=['linux-image-6.12.111+deb13-cloud-amd64'] version=6.12.111-1; state=TRYING; abi=6.12.111+deb13-cloud-amd64 pkg=linux-image-6.12.111+deb13-cloud-amd64 version=6.12.111-1; module_tree matches guest; UEFI UKI verified (vintner-os-2+3-0.efi); BIOS files verified (3/3); EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (16 entries); VINTNER_ORDER=2 1; VINTNER_T_2=xxx; staging empty; NeverAutoRemove verified: ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E3 | K2 | PASS | next boot ran os-2; image 2 GOOD (first_good=2026-10-17T05:30:53Z) |
| E4 | K2 | PASS | os-1 state=GOOD stale=false; os-1 record after op unchanged apart from last_good; os-1 record after reboot unchanged apart from last_good; os-1 UKI file intact (vintner-os-1+3-0.efi); os-1 BIOS files intact; os-1 in VINTNER_ORDER (2 1); VINTNER_T_1=xxx |
| E5 | K3 | PASS | exactly generations [1, 2, 3] present; next_generation=4; extra publish: PUBLISH_EXIT=0, stdout=['unchanged', 'vintner publish: updated apt retained kernels'] |
| E6 | U1 | PASS | op exit=0; PUBLISH_EXIT=0; publish stdout=['unchanged', 'vintner publish: updated apt retained kernels']; listings identical (16 files) |
| E7 | G1r1 | PASS | kept generations=[1, 2]; VINTNER_ORDER=2 1; EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (16 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E7 | G1r2 | PASS | kept generations=[1, 2, 3]; VINTNER_ORDER=3 2 1; EFI/Linux=['/EFI/Linux/vintner-os-1+3-0.efi', '/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-os-3+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (20 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E7 | G1r3 | PASS | kept generations=[2, 3, 4]; gen 1 record/UKI/BIOS dir/order entry/counter gone; VINTNER_ORDER=4 3 2; EFI/Linux=['/EFI/Linux/vintner-os-2+3-0.efi', '/EFI/Linux/vintner-os-3+3-0.efi', '/EFI/Linux/vintner-os-4+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (20 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E7 | G1r4 | PASS | kept generations=[3, 4, 5]; gen 1 record/UKI/BIOS dir/order entry/counter gone; gen 2 record/UKI/BIOS dir/order entry/counter gone; VINTNER_ORDER=5 4 3; EFI/Linux=['/EFI/Linux/vintner-os-3+3-0.efi', '/EFI/Linux/vintner-os-4+3-0.efi', '/EFI/Linux/vintner-os-5+3-0.efi', '/EFI/Linux/vintner-recovery-a+3-0.efi', '/EFI/Linux/vintner-recovery-b.efi']; vintner/bios as expected (20 entries); NeverAutoRemove lists ['^linux-image-6\\.12\\.111\\+deb13-cloud-amd64$'] |
| E8 | R1 | PASS | gen 2 stale=true; gen 2 kernel.package=linux-image-6.12.113+deb13-amd64 = purged package; gen 2 files present on boot storage; gen 3 state=TRYING; gen 3 kernel.abi=6.12.111+deb13-cloud-amd64; os-1 state=GOOD; reboot ran os-3 |
| E9 | all | PASS | prep(B1):os-1,report end seen,bless active; prep(B2):os-1,report end seen,bless active; C0(B1):os-1,report end seen,bless active; C0(B2):os-1,report end seen,bless active; K1(B1):os-1,report end seen,bless active,op exit 0; K1(B2):os-2,report end seen,bless active; K2(B1):os-1,report end seen,bless active,op exit 0; K2(B2):os-2,report end seen,bless active; K3(B1):os-1,report end seen,bless active,op exit 0; K3(B2):os-3,report end seen,bless active; U1(B1):os-1,report end seen,bless active; G1(BR1op):os-1,report end seen,bless active,op exit 0; G1(BR1rb):os-2,report end seen,bless active; G1(BR2op):os-2,report end seen,bless active,op exit 0; G1(BR2rb):os-3,report end seen,bless active; G1(BR3op):os-3,report end seen,bless active,op exit 0; G1(BR3rb):os-4,report end seen,bless active; G1(BR4op):os-4,report end seen,bless active,op exit 0; G1(BR4rb):os-5,report end seen,bless active; R1(B1):os-1,report end seen,bless active,op exit 0; R1(B2):os-3,report end seen,bless active |

## Findings

1. **Debian hook execution order during K1 (from apt log):**
   When `linux-image-6.12.113+deb13-amd64` was configured:
   - First, Debian ran `/etc/kernel/postinst.d/initramfs-tools`. This invoked `update-initramfs: Generating /boot/initrd.img-6.12.113+deb13-amd64`, which triggered the post-update hook `/etc/initramfs/post-update.d/zz-vintner 6.12.113+deb13-amd64`. This was the **first** run of `vintner publish`; since both `/boot/vmlinuz-6.12.113+deb13-amd64` and the newly generated `/boot/initrd.img-6.12.113+deb13-amd64` were present on disk, generation 2 was staged and published.
   - Next, Debian ran `/etc/kernel/postinst.d/zz-update-grub` (`grub-mkconfig`).
   - Next, Debian ran `/etc/kernel/postinst.d/zz-vintner 6.12.113+deb13-amd64`. This was the **second** run of `vintner publish`. In Step 6, the publisher checked the newest image with the same ABI (generation 2), compared kernel SHA-256, command line, and initramfs (all identical), printed `"unchanged"`, and updated the APT retained kernel list without creating a duplicate image.

2. **Publish execution count:**
   During K1, `vintner publish` ran **2 times**. Exactly **1** of those runs printed `"unchanged"`.

3. **Cloud kernel metapackage upgrade:**
   Debian 13 trixie published kernel version `6.12.113-1`. Because the golden disk base originally had `linux-image-cloud-amd64 6.12.111-1`, running `apt-get install linux-image-amd64` without pinning or holding would cause APT to upgrade `linux-image-cloud-amd64` to `6.12.113-1`, installing `linux-image-6.12.113+deb13-cloud-amd64` and creating an extra generation 3. Holding `linux-image-cloud-amd64` and `linux-image-6.12.111+deb13-cloud-amd64` with `apt-mark hold` during golden disk preparation ensures that only the requested second kernel (`linux-image-amd64`) is installed.

4. **Step 6 unchanged check on images with `module_tree: null`:**
   Images created at install time (such as `os-1` from `vintner-base.sh`) have `module_tree: null`. In Step 6 of `vintner publish`, when `module_tree` is nil, kernel SHA-256 is checked against `BIOS.Files.Vmlinuz` (or UKI `.linux` section) instead of assuming a non-nil `ModuleTree`, ensuring that no-op publishing (`vintner publish --kernel $(uname -r)`) on `os-1` in Case U1 correctly recognizes identical inputs and prints `"unchanged"`.

5. **agetty can cut off the report on ttyS0.** In one run the report of K2's operation boot stopped after `LOADER_VARS`: `serial-getty@ttyS0` started mid-report, agetty hung up the tty (its terminal probe is in the log), and the report's later writes were lost, so E9 failed although the boot was fine. `ci/lib/vintner-base.sh` now orders the report after the gettys and stops the ttyS0 getty before the report opens the console.
