# WO-0010: Review findings and product readiness (phased)

- **For:** the agents that fix the review findings below and make vintner-boot usable by a first product OS (WineExplorer, a Debian 13 desktop). Read `docs/architecture.md` §3 (requirements), §4–§6, `docs/adr/0003-uefi-entry-naming-and-boot-counting.md` and `docs/security-threat-model.md` first.
- **Issued:** 2026-11-07
- **Status:** open. Decisions recorded 2026-11-07 (see "Decisions").
- **Branch:** one branch and PR per phase, `wo-0010-p<N>` from `origin/main`. A phase starts after the previous one is merged, except where the dependency table says otherwise. Large phases may be split into several PRs; keep the item IDs in the PR titles.
- **Basis:** a review on 2026-11-07 of `main` at `2dae442`, the unmerged branches `review-fixes` (`5d5f735`) and `recovery-signing` (`a2f5df9`), and the needs of WineExplorer (its `docs/VINTNER_BOOT.md`). Every finding below was checked against the code; file:line references are to `main` at `2dae442`.

## Goal

Close the gaps between what the documents promise and what the code does, then add the pieces a product needs to adopt vintner-boot: real packages, an installer entry point, product naming, kernel command-line management, extra boot entries (such as Safe Mode) and restart into recovery.

## Priorities

- **P0:** breaks or leaves unproven a release requirement (R01–R14, architecture §3).
- **P1:** needed before a product can ship on vintner-boot, or an architecture §3 P1 item a product depends on.
- **P2:** consistency, documentation or test coverage.

| Phase | Theme | Highest priority | Depends on |
|---|---|---|---|
| 0 | Land pending work, correct the record | P1 | — |
| 1 | Correctness of the P0 guarantees | P0 | 0 |
| 2 | Debian packaging | P0 | 0 (can run in parallel with 1) |
| 3 | Installer entry point | P0 | 2 |
| 4 | Product integration surface | P1 | 3 (4.1 and 4.6 can start after 2) |
| 5 | Secure Boot completion | P1 | 3 |
| 6 | Verification gaps | P0 (6.1), else P2 | runs alongside 3–5 |

## Rules

- Go standard library only, `CGO_ENABLED=0`, SPDX header on every new file, the style of the surrounding code (small packages, interfaces for external tools so tests use fakes).
- Every behaviour change gets a test that fails without the change. Say in the PR how you showed that.
- No change to a P0 guarantee without a QEMU row in `.github/workflows/qemu-tests.yml` that exercises it.
- Documents and code change together: when an item changes a documented behaviour, update `docs/architecture.md`, the ADR or the interface file in the same PR. If an item conflicts with an accepted ADR, stop and report instead.
- PR body as a file; commits and the PR end with the usual attribution lines.

## Decisions

Recorded 2026-11-07. The owner decided D-A and D-D. For the others the owner set the goal: the boot and recovery experience should feel familiar to people who know Windows. The reviewer chose the options below to serve that goal. The owner may override any of them; an item that depends on an overridden decision stops and reports.

- **D-A (decided): the product's installer partitions.** It creates the layout documented in 3.1. `vintner install` validates the layout and deploys onto it; it never partitions.
- **D-B (chosen): the kernel command line lives in `/etc/kernel/cmdline`.** It holds the product's and the administrator's options; the publisher adds `root=` and `vintner.bootstorage=` itself, and `vintner cmdline` edits the file (4.2). The Windows equivalent is `boot.ini`, which XP users edit from Startup and Recovery or msconfig. A change becomes a new `TRYING` generation, so a bad option falls back by itself.
- **D-C (chosen): each variant is its own UKI without a boot counter.** D-D rules out multi-profile UKIs: systemd-boot keeps the counter in the file name, so every profile of one file shares one counter, and a Safe Mode boot would spend a try of the normal entry. On BIOS a variant is a second command line in the same slot and costs no space.
- **D-D (decided): a variant boot changes no boot state.** A `GOOD` image stays `GOOD`, a bad image stays bad, and a `TRYING` image keeps exactly the tries it had. Booting a variant never re-arms, promotes or demotes any image, including one whose ordinary boot failed just before.
- **D-E (chosen): vintner does not own `/etc/plymouth/plymouthd.conf`.** `vintner-core` ships its theme only. `vintner install --plymouth-theme <name>` (default `vintner-bgrt`) selects the boot screen once, so a product installs its own boot screen the same way. On Windows the boot screen belongs to the system: XP's logo and progress bar, or since Windows 8 the PC maker's logo, which `vintner-bgrt` already reproduces.
- **D-F (chosen): the interface is `vintner status --json` and `--json` output on the other commands, not a D-Bus service.** ADR-0002 allows only the Go standard library, which has no D-Bus. A product that runs its settings UI unprivileged wraps these commands in its own privileged service. That is enough for Windows-style dialogs: Startup and Recovery, msconfig's boot options, "restart in Safe Mode".
- **D-G (chosen): tier B on machines that need out-of-tree modules.** Windows users expect an installed driver to work with Secure Boot on. With tier B the machine key is enrolled once as a MOK (one confirmation in MokManager on the first boot) and DKMS signs modules with the same key, so vendor drivers load. Tier A stays for managed machines; there the installer and `vintner status` say plainly that out-of-tree modules cannot load with Secure Boot on (5.3).

### The Windows-familiar boot experience

What vintner provides so that a product can offer the start-up experience Windows users know. The product's own screens are the product's work, not this work order's.

| Windows XP | Here | Who |
|---|---|---|
| No menu when one system is installed; F8 during start shows the options | Hidden menu (`timeout 0`); a key held at start opens it; F8 is the documented key (4.8) | vintner |
| "Windows Advanced Options Menu": Safe Mode, Safe Mode with Networking, Safe Mode with Command Prompt, Enable Boot Logging, Enable VGA Mode, Last Known Good Configuration, Debugging Mode, Disable automatic restart, Start normally, Reboot, Return to OS Choices Menu | One variant entry per generation, titled by the product (for example "Advanced Options"), that boots into the product's own text menu, which applies the choice | variant: vintner (4.3); menu: product |
| Enable VGA Mode | A second variant with `nomodeset`, or the product's menu blocking GPU drivers for that boot | product's choice |
| Last Known Good Configuration | The newest `GOOD` generation older than the newest one: `vintner next-boot previous-good` (4.4); older generations also stay in the menu | vintner |
| Return to OS Choices Menu | `vintner next-boot menu` (4.4) | vintner |
| "Windows did not start successfully" screen after a failed start, with a 30-second countdown | `vintner status --json` reports the booted image's failed attempts since its last good boot (4.5); the product shows the screen early in boot | data: vintner; screen: product |
| Recovery Console | Recovery A and B, titled by the product (4.1) | vintner |
| Startup and Recovery: "Time to display list of operating systems", Edit `boot.ini` | `vintner menu-timeout` (4.7) and `vintner cmdline` (4.2) | commands: vintner; dialog: product |
| msconfig BOOT.INI tab (`/SAFEBOOT`, `/BASEVIDEO`, `/SOS`, `/BOOTLOG`, `/NOGUIBOOT`) | `vintner next-boot variant <id>` for one boot; `vintner cmdline` for lasting options | both |
| The last known good configuration is updated after a successful logon | The product's health check passes once the logon screen is up (4.6) | product |

## Phase 0: land pending work and correct the record

**0.1 (P1) Merge `review-fixes` (`5d5f735`).** It merges cleanly into `main` and is pushed. It makes the publisher's "unchanged" check verify the files on boot storage (a damaged or unfinished image was otherwise skipped for ever), quotes `slot.cfg` with GRUB rules instead of Go's `%q`, accepts only canonical generation numbers in `bls.IsVintnerOrdinary`, and makes recovery choose the root from the manifest that names the `vintner.bootstorage=` partition. Review it, open a PR, and confirm the `publish`, `bios-publish`, `recovery-fallback` and `selection-policy` rows pass.

**0.2 (P1) Merge `recovery-signing` (`a2f5df9`).** It merges cleanly and adds `--key`/`--cert` signing and `--uki-only` to `recovery/build.sh`. Same procedure; the `secure-boot` row must pass and show a signed recovery UKI verifying with `sbverify`.

**0.3 (P2) Stale status text.**
- `README.md:5` says "no code yet".
- `docs/secure-boot.md:3` says "Nothing here is implemented yet"; tier A is implemented (WO-0009c).
- `graphics/plymouth-theme/README.md:36` says the package installs the initramfs hook; `debian/vintner-core.install` does not (phase 2 fixes the packaging; until then the README must say so).
- `integration/initramfs-hooks/README.md:7` calls garbage collection "publish step 14". It is step 13 (`src/internal/publish/publish.go:951`); step 14 is the APT retention list (`publish.go:1020`), as `integration/kernel-hooks/README.md:9` says.

**0.4 (P2) Schema examples the publisher would refuse.** Every image `cmdline` in `schema/examples/manifest-uefi.json` (lines 33, 57, 81) and `manifest-uefi-tier-a.json` (36, 61, 86) lacks `vintner.bootstorage=PARTUUID=<boot_storage.partuuid>`, which `publish.go:458-479` requires. Fix the examples and add a test that runs every example image's command line through the same check the publisher uses (move the check into a function if needed).

**0.5 (P2) `vintner_title`.** `firmware/bios/INTERFACE.md:30` documents an optional `vintner_title` in `slot.cfg`; nothing writes or reads it. Mark it "reserved, see WO-0010 4.1" until 4.1 implements it.

## Phase 1: correctness of the P0 guarantees

**1.1 (P0, R05/R14) Recovery A is never re-armed.** ADR-0003:63 and `firmware/bios/INTERFACE.md:72` say the recovery image re-arms recovery A once its early checks pass. Nothing does: `vintner bless` exits on a recovery entry (`src/cmd/vintner/bless.go:88-90` on UEFI, `bless.go:115-116` for `VINTNER_BOOTED=RA` on BIOS), and `recovery/initramfs/init` never touches the counter. Recovery A therefore has `tries` boots for the life of the machine; after that it sorts as bad on UEFI and is skipped on BIOS, and only the uncounted recovery B remains.
- Change: after the early checks (define them in the PR: boot storage found and mounted read-write is the minimum), recovery re-arms recovery A: on UEFI, rename `vintner-recovery-a+<left>-<done>.efi` back to `+<tries>-0`; on BIOS, set `VINTNER_T_RA` to `tries` and clear `VINTNER_BOOTED`. Use the `vintner` binary from 1.2 (`vintner bless --recovery`, or a new subcommand), not shell code, so the grubenv format has one implementation.
- Recovery must know whether it is A or B: replace the unused `vintner.recovery=1` (`recovery/build.sh:77`; `init` never reads it) with `vintner.recovery=a` and `vintner.recovery=b`. Recovery B never writes.
- Acceptance: extend `ci/recovery-fallback` so recovery A boots `tries + 1` times in a row on both firmwares and every boot reaches the recovery shell. The test fails on `main`.

**1.2 (P1) The `vintner` binary is not in the recovery image.** ADR-0002:44 and `schema/manifest.md:3` say recovery uses the same binary as the host. `recovery/build.sh` does not include it (`recovery/BUILDING.md:94-98`). Add the static binary, record its version in `recovery/out/versions.txt`, keep the image within the §5 budget, and report the size change.

**1.3 (P0, R10) BIOS GRUB loads a module that `core.img` does not contain.** `firmware/bios/grub.cfg:9` runs `insmod all_video` on every boot. `firmware/bios/INTERFACE.md:11` lists the modules embedded in `core.img`, without `all_video`, and says GRUB loads no module from disk. `ci/lib/vintner-base.sh:201-202` embeds it only when `VINTNER_EXTRA_CMDLINE` is set, so the CI boots without it never exercise this path. Boot storage has no module directory, so the `insmod` fails and prints an error on an otherwise silent boot.
- Change: add `all_video` (and any other module `grub.cfg` uses) to the documented list and always embed it. Add a check that compares every `insmod` and command in `grub.cfg` with the embedded module list.
- Acceptance: a BIOS boot built without `VINTNER_EXTRA_CMDLINE` shows no `error:` line on the serial console.

**1.4 (P1, R10) Serial terminal on machines without a serial port.** `firmware/bios/grub.cfg:6-8` always sets `serial --unit=0` and serial input and output. Boot a BIOS VM with `-serial none`. If GRUB prints anything, make the serial setup conditional (for example on the `serial` command succeeding) without losing serial output in CI.

**1.5 (P1, T6) The side-loaded file check ignores the ESP.** The threat model (`docs/security-threat-model.md:116`) requires reports for `\loader\addons\`, `\loader\credentials\` and `*.extra.d` on the ESP or boot storage. `state.Scan` takes only the boot storage path (`src/internal/state/state.go:47`). Scan the ESP too, read-only, found by partition identity rather than by `/boot/efi` being mounted. Acceptance: a file planted in `\loader\addons\` on the ESP is reported by `vintner bless`.

**1.6 (P2) The `systemd-gpt-auto-generator` check only observes.** Architecture §5 (`docs/architecture.md:122`) asks CI to confirm that the generator does not mount boot storage. `ci/integration/uefi-bless/run.sh:523-527` records this as `OBSERVED`. Turn it into a pass/fail check: fail if any generated mount unit names the boot storage partition.

## Phase 2: Debian packaging

Today `debian/vintner-core.install` ships only the Plymouth theme and `plymouthd.conf`. The binary, hooks, unit, masks and `/etc/kernel/install.conf` are installed by hand in `ci/lib/vintner-base.sh:278-294`. No product can install vintner-boot from packages.

**2.1 (P0, R02/R03) `vintner-core` installs everything the platform needs on the host.**
- `Architecture: amd64` (it is `all` in `debian/control:10`), built with `CGO_ENABLED=0` from `src/` (trixie ships Go 1.24).
- `/usr/sbin/vintner`; `/etc/kernel/postinst.d/zz-vintner`; `/etc/initramfs/post-update.d/zz-vintner`; the Plymouth initramfs hook under `/usr/share/initramfs-tools/hooks/` (packages do not ship hooks in `/etc/initramfs-tools/hooks/`); `vintner-bless.service` through `dh_installsystemd`.
- `systemd-bless-boot.service` and `systemd-boot-update.service` must stay inactive. Explain in the PR how the package achieves this and what happens when the package is removed.
- `/etc/kernel/install.conf` (`layout=other`): check its owner with `dpkg -S` on trixie and do not overwrite another package's conffile.

**2.2 (P0) Package relations.** Add the `Conflicts` from architecture §5 (`docs/architecture.md:146-155`) and `vintner-core Depends: systemd-ukify`. `ci/lifecycle/packages/README.md` (findings 1 and 4) shows that each of `systemd-boot`, `grub-efi-amd64`, `grub-cloud-amd64` and `grub-pc` replaces a vintner loader file when installed.

**2.3 (P0) `/etc/plymouth/plymouthd.conf` (D-E).** `vintner-core` ships this path, and Debian's `plymouth` may ship it too. CI hides the question by installing with `--force-confold` and copying the file over (`ci/graphics/run.sh:280-281`).
- Remove `plymouthd.conf` from `debian/vintner-core.install`.
- Theme selection moves to `vintner install --plymouth-theme` (3.2). It sets `Theme`, `ShowDelay=0` and `DeviceTimeout=8` in `/etc/plymouth/plymouthd.conf` as an administrator edit, keeping every other line.
- Establish with `dpkg -S` on trixie whether `plymouth` ships the file as a conffile, and report what an upgrade of `plymouth` does to the edited file under `unattended-upgrades`.
- Acceptance: `vintner-core` installs before and after `plymouth` with no dpkg prompt or error. After `vintner install`, the chosen theme is active and survives a `plymouth` reinstall.

**2.4 (P1) `vintner-platform` payloads.** Ship the inert payloads described in `debian/control` under `/usr/lib/vintner/payloads/`: the systemd-boot binary, the GRUB `boot.img` and the module set for `core.img`, and the recovery UKIs or their parts. Installing or upgrading the package never touches the ESP, boot storage or NVRAM. Phase 3 deploys from here.

**2.5 (P1) CI installs from the packages.** Replace the hand installation in `ci/lib/vintner-base.sh:278-294` with the built `.deb` files, and run the package-lifecycle rows (`lifecycle-uefi`, `lifecycle-bios`) against them.

## Phase 3: installer entry point (R01, R13; WP-007)

**3.1 (P1) The layout contract.**
- Write `docs/accepted-platforms.md`, which architecture §11 lists (`docs/architecture.md:276`) but which does not exist.
- Fill `examples/disk-layouts/`, which is a one-line placeholder. It needs: partition type GUIDs, minimum and default sizes, FAT label conventions (identity is by PARTUUID or UUID), the BIOS Boot Partition, the rule that an existing ESP is reused and never resized, and single-disk scope.
- State what a root on md RAID, LVM or LUKS means for boot storage (always a plain partition on one disk).

**3.2 (P0) `vintner install`.** One command that turns a prepared target into a vintner system. It must run from inside a chroot of the target, where `/proc/cmdline` belongs to the installer's kernel. Today both `publish` (`publish.go:314-337`) and `cmd/vintner/bootstorage.go:20` read `/proc/cmdline`.
- Inputs, illustrative names: `--target`, `--esp`, `--boot-storage`, `--bios-disk`, `--firmware uefi|bios|both`, `--tier none|A|B`, `--plymouth-theme` (2.3), `--recovery-title` (4.1), and the product's command-line options in the target's `/etc/kernel/cmdline` (D-B). Partitioning is the product's (D-A): `vintner install` checks the layout against 3.1 and refuses a layout that does not match.
- Order, from architecture §5 (`docs/architecture.md:130-133`):
  1. Deploy and verify the loader, `loader.conf`, `boot.img`/`core.img`, grubenv and the recovery images from the 2.4 payloads.
  2. Write the manifest with the target's `machine_id` and a new `installation_id`. In tier A, run `vintner secureboot init` and sign the loader and recovery.
  3. Publish generation 1 with an explicit command line and root identity.
  4. Remove `\EFI\debian`, then purge the stock loader packages (`--allow-remove-essential`, because `grub-efi-amd64-signed` is `Protected: yes`; lifecycle findings 1–2).
  5. Verify again.
- On any failure it exits non-zero, says what it changed, and leaves the stock boot path working.
- Acceptance: `ci/lib/vintner-base.sh` uses `vintner install` instead of its own steps (3.6), and every existing QEMU row still passes.

**3.3 (P1, R13) NVRAM boot entry.** Nothing creates a `Boot####` entry: `efibootmgr` does not appear in `src/` and `ci/` relies on the removable path. `vintner install` creates an entry for `\EFI\vintner\systemd-bootx64.efi`, puts it first in `BootOrder` (through `src/internal/efivar`, which can write), and writes `\EFI\BOOT\BOOTX64.EFI` only when no other operating system uses the ESP (R13).

**3.4 (P1, T8) `BootOrder` health report.** The threat model (`docs/security-threat-model.md:137`) requires a report when `BootOrder` no longer starts with the vintner entry, or the entry is missing. Implement it once 3.3 exists.

**3.5 (P1) Initramfs freshness at install time.** Products that install by copying a live system carry the live initramfs (WineExplorer does). `vintner install` refuses to publish generation 1 unless each kernel's initramfs is newer than the last change to the installed initramfs hooks, or it runs `update-initramfs -u -k all` itself. Document which.

**3.6 (P2) CI uses the product path.** Covered by the 3.2 acceptance. Keep the debugging hooks the current script offers (`VINTNER_EXTRA_CMDLINE` and similar) as options of the CI wrapper, not of `vintner install`.

## Phase 4: product integration surface

**4.1 (P1) Product names in menus.** Ordinary UEFI entries already use `PRETTY_NAME` from the installed `/etc/os-release` (ADR-0003:39). The rest is fixed text:
- BIOS: `vintner os <g>`, `vintner recovery A`, `vintner recovery B` (`firmware/bios/grub.cfg:65-70`).
- Recovery UKIs: `vintner recovery A|B (<version>)` (`recovery/build.sh:73`).

Change:
- No visible menu text contains "vintner" unless the product chooses it.
- Ordinary entries show `PRETTY_NAME` and the publish date, for example "WineExplorer (2026-11-07)", so a person picks a generation by date, as Windows users pick a restore point. The ABI moves out of the title into a field shown on request (systemd-boot's `p` key). Amend the ADR-0003 table.
- BIOS: the publisher writes `vintner_title` (and one title per variant, 4.3) into `slot.cfg`, and `grub.cfg` uses them.
- Recovery entries default to "Recovery Console" (A) and "Recovery Console (backup)" (B). `vintner install --recovery-title` sets a product title. It rebuilds the recovery `.osrel` from the payload parts and signs in tier A or B; recovery stays independent of the installed kernel (R06).
- Acceptance: CI asserts the menu titles on both firmwares.

**4.2 (P1) Kernel command-line management.** Each publish copies the command line of the newest `GOOD` image (`publish.go:436-456`). Nothing reads `/etc/kernel/cmdline`, and no command changes the line after installation. Desktop users need `nomodeset`, vendor driver options or a serial console.
- Change, per D-B: the publisher composes `root=… ro vintner.bootstorage=…` plus the contents of `/etc/kernel/cmdline`, refusing `root=` and `vintner.*` tokens there.
- `vintner cmdline show|set` edits the file and publishes a new generation for the running kernel ABI. A command-line change is therefore an ordinary `TRYING` image, and a bad option falls back to the previous `GOOD` image with the old line (R04: no in-place overwrite).
- Acceptance: a QEMU row sets an option that prevents booting, and the machine returns to the previous generation after `tries` boots.

**4.3 (P1) Extra boot entries (variants).** Products need entries that add command-line tokens, for example Safe Mode (`wexmode=safecmd`) or a basic-video mode (`nomodeset`). Today there is no way:
- The command line is inside each UKI (signed in tier A) and `editor no` is set.
- `firmware/bios/grub.cfg` is static.
- Any other entry is reported as foreign.

Change (D-C, D-D):
- A product declares variants in `/etc/vintner/variants.d/<id>.conf`: a title and extra tokens, validated like 4.2. vintner adds `vintner.variant=<id>` to each variant's command line.
- UEFI: for each retained generation and each variant, the publisher builds one more UKI: the generation's command line plus the variant's tokens, signed in tier A or B.
  - It has **no boot counter** in its name, like recovery B.
  - Its file name must not match the `loader.conf` `default` pattern, so systemd-boot never selects a variant on its own. Otherwise, with every ordinary image bad, an uncounted variant of a broken kernel would boot for ever instead of recovery.
  - Its sort key places it directly after its generation's ordinary entry.
  - Amend ADR-0003: names, order and the foreign-entry rules.
- BIOS: `slot.cfg` holds one command line per variant, and the Shift menu lists the variants under their generation. A variant menu entry neither decrements a counter nor sets `VINTNER_BOOTED`.
- Bless: a boot whose command line has `vintner.variant=` changes nothing: no rename, no grubenv write, no manifest state change. This includes a `VINTNER_BOOTED` left by a failed ordinary boot just before (BIOS); it must stay so that the next ordinary boot counts the failure.
- Manifest and storage:
  - Each image records its variant files and their hashes.
  - Health treats variant files as vintner's.
  - Garbage collection removes them with their generation.
  - The §5 image budget counts them. If the variants do not fit, the publisher reports it and publishes the ordinary image without them. It never drops a `GOOD` ordinary image or recovery to make room.
- Acceptance, on both firmwares:
  - Booting a variant of a `GOOD`, a `TRYING` and a bad image leaves every file name, grubenv variable and manifest state byte-identical.
  - A variant boot right after a failed ordinary boot leaves that failure counted.
  - With every ordinary image bad and variants present, recovery A boots without input (`ci/selection-policy`).
  - The menu shows each variant directly after its generation.

**4.4 (P1) Restart into recovery or a chosen entry.** Architecture §3 P1 asks for one-shot recovery from the running system, and §11 names `set_recovery_next_boot` (`docs/architecture.md:54`, `:308`). No command sets `LoaderEntryOneShot`, and `state.go:459-460` reports it as foreign whenever it is set.
- Add `vintner next-boot` with these targets:
  - `recovery`: the Recovery Console.
  - `previous-good`: the newest `GOOD` generation older than the newest generation (Windows' Last Known Good Configuration).
  - `generation <g>`.
  - `variant <id>`: that variant of the generation that would boot next ("restart in Safe Mode").
  - `menu [--timeout <s>]`: show the menu once (Windows' "Return to OS Choices Menu").
- UEFI uses `LoaderEntryOneShot` and `LoaderConfigTimeoutOneShot`. BIOS uses one-shot grubenv variables that `grub.cfg` consumes and clears in the same `save_env` as its counter write.
- A one-shot boot of an ordinary generation counts like any boot; a one-shot variant follows D-D.
- The health check must not report a one-shot setting that vintner made.

**4.5 (P1) Machine-readable status (D-F).**
- `vintner status --json` reports:
  - each generation with its state, publish date, ABI and variants;
  - the booted generation and variant;
  - the booted image's failed attempts since it last booted good, not counting this boot. On UEFI this comes from the `+<left>-<done>` name in `LoaderBootCountPath`; on BIOS, from the counter.
  - health reports, tier and firmware;
  - whether out-of-tree modules can load with Secure Boot on.
- The failed-attempts field must be readable early in boot, before the display manager starts, so a product can show a "did not start successfully" screen.
- Every command a product calls (`status`, `next-boot`, `cmdline`, `menu-timeout`) gets `--json` output and documented exit codes. This covers architecture §3 P1, "common status/reporting commands". Products build their UI on it.

**4.6 (P2) Health-check contract for products.** Document how a product adds its own checks: units `Before=boot-complete.target`, pulled in by `RequiredBy=boot-complete.target` (`docs/architecture.md:91`). Cover:
- checks that must pass during first-boot setup phases, when the product deliberately holds back its desktop;
- variant boots;
- short `Description=` lines, because systemd cuts status lines at 80 columns on serial (`ci/recovery-fallback/README.md:130`).

Recommend to products the Windows rule: XP updates its last known good configuration after a successful logon, so a desktop product's check passes once its logon screen (or its first-boot setup screen) is shown.

**4.7 (P2) Menu timeout.** XP's Startup and Recovery dialog sets "Time to display list of operating systems".
- `vintner menu-timeout <seconds>` sets it on both firmwares: `timeout` in `loader.conf` (vintner's file, written atomically) and a grubenv variable that `grub.cfg` honours. 0 keeps the hidden menu.
- Health keeps reporting a `LoaderConfigTimeout` set from the menu keys (`+`/`-`), but not vintner's own setting.

**4.8 (P2) The menu key.** Windows users press F8.
- UEFI: systemd-boot opens its menu when a key is held at start (`firmware/uefi/loader.conf:7`). Confirm on reference hardware (6.1) that F8 works, and document F8.
- BIOS: GRUB's `keystatus` tests only Shift, Ctrl and Alt, and `grub.cfg` uses `keystatus --shift`. Spike a zero-delay way to detect F8. If there is none, keep Shift on BIOS and document both keys.
- Do not add a boot delay to catch a key (R10).

## Phase 5: Secure Boot completion

**5.1 (P1) Tier A end to end.**
- The installer signs the loader and recovery (WO-0009c listed these as follow-ups), using 0.2 and 3.2.
- Answer on real hardware whether the vendor's `KEK` and `db` must be kept when the machine key is enrolled (`docs/secure-boot.md:53`, `:101`).

**5.2 (P1) Tiers B and C.** `publish.go:407-409` refuses everything except `none` and `A`. Tier A needs the firmware in setup mode, which ordinary PCs are not, so a product cannot support Secure Boot on ordinary hardware without tier B (MOK, one MokManager confirmation) or C (Debian-signed chain). Design and implement B first. Include the Microsoft 2011/2023 CA rows and SBAT/DBX levels from architecture §10.

**5.3 (P1) Out-of-tree modules (D-G).** Desktop products need DKMS modules, for example vendor GPU drivers.
- In tier A there is no shim, so no MOK list. Verify on Debian 13's kernel that Linux does not accept `db` keys (the platform keyring) for module signatures. If it does not, DKMS modules cannot load under Secure Boot in tier A.
- Tier B (with 5.2): enroll the machine key as a MOK during installation, so there is one MokManager confirmation on the first boot. Configure DKMS to sign with that key (a drop-in under `/etc/dkms/`), keeping the key off the ESP and boot storage (R11).
- Tier A: `vintner install` and `vintner status` state that out-of-tree modules cannot load with Secure Boot on.
- A DKMS rebuild changes the module tree, and `schema/manifest.md:62` then marks the image `stale`. Instead, the rebuild must lead to a newly published generation, and the stale image must not be left as the only choice.
- Acceptance: on tier B in QEMU, a DKMS test module builds, is signed and loads with Secure Boot on. On tier A, `vintner status` reports that it cannot.

## Phase 6: verification gaps

**6.1 (P0) Real hardware.** WO-0001 Part B and `ci/hardware/README.md:126` are pending. Cover:
- the key-press menu with `timeout 0` on real firmware (only QEMU's Space key is proven);
- power cuts and sector atomicity on boot storage (architecture §12 risk 3).

**6.2 (P2) systemd-boot hook lifecycle.** Steps 2–7 of `ci/lifecycle/systemd-boot-hooks` were never run (`results.md:11-16`). Run them or record why they no longer apply.

**6.3 (P2) Debian testing lane.** Architecture §10 (`docs/architecture.md:255`) asks for a non-blocking forky lane; `.github/workflows/qemu-tests.yml` has none.

**6.4 (P1) Root storage lanes.** Architecture §10 (`:257`) lists LUKS, LVM and Btrfs root as separately tested features. None is tested for the normal boot path; products that offer md RAID roots need at least md and LUKS.

**6.5 (P2) Threat-model verify items.** Resolve T6 and T12 (`docs/security-threat-model.md:203`); 1.5 covers part of T6.

## Validation before you push (every phase)

- In `src/`: `gofmt -l .` clean, `go vet ./...`, `go test ./...`, `go test -tags vintner_testpoints ./internal/testpoint/`, `CGO_ENABLED=0 go build ./...`.
- `shellcheck` on every changed shell file.
- The QEMU rows the phase touches, locally where possible (`ci/local/run-all.sh`) and in CI. Quote the result lines.
- For each new behaviour, show the test failing without the change.

## Out of scope (do not do)

- Phase 7 of the architecture (transactional OS, root slots, RAUC), Limine, TPM and dm-verity.
- Product-specific code. WineExplorer's installer and Safe Mode changes are tracked in WineExplorer's `docs/VINTNER_BOOT.md`.
- Changing an accepted ADR without the owner's decision.

## Reporting back

- Per PR: item IDs, what changed, the "fails without the change" proofs, QEMU rows run and their results, and the decisions (D-A to D-G) the PR relied on.
- Anything that cannot be done as written, with the exact reason.

## Appendix: notes on the review

- Two survey claims were corrected during checking:
  - A `systemd-gpt-auto-generator` check does exist, but it only observes (1.6).
  - The systemd-boot hook steps not run are in `results.md:11-16`, not line 78.
- The WineExplorer requirements map to items as follows:

  | WineExplorer requirement | Items |
  |---|---|
  | Safe Mode entries | 4.3 |
  | Command-line changes | 4.2 |
  | Names | 4.1 |
  | Packages | 2.1–2.4 |
  | Chroot install | 3.2, 3.5 |
  | Restart into recovery | 4.4, 4.5 |
  | Secure Boot on ordinary PCs | 5.2 |
  | Recovery A re-arm | 1.1 |
  | ESP scan | 1.5 |
  | Recovery binary | 1.2 |
