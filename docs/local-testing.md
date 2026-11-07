# Local testing on Windows with WSL2 and VirtualBox

This is how to run the project's tests on a Windows workstation, as CI does. Each piece runs in its own place:

| Where | What |
|---|---|
| Windows checkout, `D:\Projects\vintner-boot` | Editing, git, `gh`. |
| WSL2 distribution `Debian` (Debian 13) | Go unit tests, `shellcheck`, and every QEMU suite in `.github/workflows/qemu-tests.yml`, with KVM through nested virtualization. |
| Clone in the WSL filesystem, `~/vintner-boot` | Where the QEMU suites run. Its `origin` is the Windows checkout. |
| VirtualBox 7.2 on Windows | Dry runs of the real-hardware images (`ci/hardware/`): one more firmware implementation, still virtual. |

Before pushing, run the local gate, which runs everything CI runs ([below](#the-local-gate-everything-ci-runs)). Check a machine at any time with:

```bash
ci/local/check-env.sh --loop
```

It checks the packages, firmware files, Go version, KVM, sudo, loop devices with partition scanning, the git identity and free space. It exits 1 if anything required is missing.

## One-time setup

### 1. WSL2 with Debian 13 and KVM

In PowerShell:

```powershell
wsl --install -d Debian
```

`cat /etc/debian_version` must show 13.x (trixie). The suites take their Debian packages from trixie and expect trixie's QEMU, OVMF and `systemd-ukify` on the host.

On Windows 11, WSL2 exposes `/dev/kvm` by default when the CPU's virtualization (VT-x or AMD-V) is enabled in the firmware setup. If `/dev/kvm` is missing, add this to `%USERPROFILE%\.wslconfig` and run `wsl --shutdown`:

```ini
[wsl2]
nestedVirtualization=true
```

Without KVM, the suites fall back to QEMU's TCG emulation, which is many times slower, and long boots can run past the suites' timeouts.

### 2. Packages

Inside WSL, install what `lint.yml` and `qemu-tests.yml` install, plus Go and `shellcheck`:

```bash
sudo apt-get update
```

```bash
sudo apt-get install -y golang-go shellcheck curl qemu-system-x86 qemu-utils ovmf seabios mtools fdisk e2fsprogs kmod cpio xz-utils python3-pefile python3-virt-firmware sbsigntool cloud-image-utils binutils systemd-boot-efi systemd-ukify grub-common build-essential debhelper dpkg-dev lintian
```

trixie's `golang-go` is Go 1.24, which is what `src/go.mod` asks for. If `go.mod` moves to a newer Go, install that from go.dev.

### 3. Your user: KVM, sudo, never root

- **KVM group:** run `sudo usermod -aG kvm "$USER"`, then `wsl --shutdown` from Windows so the new group applies.
- **sudo:** the suites call `sudo` partway through a run, for `losetup` and `mount`. The local gate (below) runs jobs with no terminal, so it needs sudo without a password. That's reasonable in a distribution kept for testing: `echo "$USER ALL=(ALL) NOPASSWD: ALL" | sudo tee /etc/sudoers.d/90-$USER`. To run a single suite by hand with a password-protected sudo, run `sudo -v` just before starting it, or the run stops at a prompt.
- **Never root:** run the tests and git as your own user, never as root or through `sudo git`.

### 4. Git identity in WSL

```bash
git config --global user.name zcomputerwiz
```

```bash
git config --global user.email zcomputerwiz@users.noreply.github.com
```

Without these, git signs commits `<user>@<hostname>.localdomain`. GitHub then credits them to whichever account has claimed that address. Commits `05ab424` and `532252c` were made this way, as `root@MSI.localdomain`, and show up on GitHub under an unrelated account.

### 5. A clone in the WSL filesystem

The QEMU suites write multi-gigabyte sparse disk images and attach them with `losetup -P`. On the Windows drive (`/mnt/d`, drvfs) that is slow, and sparse files and loop devices are unreliable. So run the suites from a clone on WSL's own ext4 filesystem:

```bash
git clone /mnt/d/Projects/vintner-boot ~/vintner-boot
```

Its `origin` is the Windows checkout, so it sees local branches without a push.

Git refuses to work in `/mnt/d/...` from WSL because Windows owns the files. Allow just this checkout:

```bash
git config --global --add safe.directory /mnt/d/Projects/vintner-boot
```

`safe.directory=*` also works, but it turns the ownership check off for every repository.

## Everyday use

### Unit tests and lint

These are fast and need no loop devices, so they can run straight on the Windows checkout. From PowerShell:

```powershell
wsl -d Debian -e sh -c 'cd /mnt/d/Projects/vintner-boot/src && test -z "$(gofmt -l .)" && go vet ./... && go test ./... && go test -tags vintner_testpoints ./internal/testpoint/'
```

```powershell
wsl -d Debian -e sh -c 'cd /mnt/d/Projects/vintner-boot && git ls-files -z "*.sh" | xargs -0 -r shellcheck'
```

Use `wsl -e`, as above. `wsl -- <command>` passes the command through your login shell first, which expands `$variables` before `sh -c` sees them.

### The local gate: everything CI runs

[`ci/local/run-all.sh`](../ci/local/run-all.sh) runs every CI job in the WSL clone, one at a time: the `lint.yml` jobs, the `qemu-baseline.yml` boots and every `qemu-tests.yml` suite. It ends with a pass/fail table and exits 1 if any job failed. Run it before pushing:

```bash
cd ~/vintner-boot && ci/local/run-all.sh --ref <branch>
```

How it works:
- `--ref <branch>` fetches from `origin` (the Windows checkout) and switches the clone to that branch, detached. Without it, the gate tests whatever the clone has checked out.
- The QEMU job list is read from the workflow files, so it follows CI. `--list` prints it.
- To run part of the gate, name jobs or groups (`lint`, `baseline`, `qemu`): for example `ci/local/run-all.sh lint publish-uefi interrupt-bios`.
- As in CI, each job starts without its earlier output and is judged by its exit status.
- Each job runs with no terminal, so a prompt that CI never sees can't stall the run. For the same reason the gate needs sudo without a password.
- Each job has a time limit (`--timeout`, default 120 minutes). A job that runs over is stopped and marked TIMEOUT, and any loop devices and mounts it left under `ci/out/` or `recovery/out/` are released. Ctrl-C does the same for the running job.
- Logs and `summary.tsv` go to `ci/out/local/run-all/<time>/`.

### QEMU suites one at a time

Bring the WSL clone to the branch you want to test. Changes not committed on Windows are not in it:

```bash
cd ~/vintner-boot && git fetch origin && git switch --detach origin/<branch>
```

Then run a suite as its CI job does. Most build the recovery image first:

| CI job | Command |
|---|---|
| `selection-policy` | `ci/selection-policy/run.sh` |
| `bios-trial` | `ci/bios-trial/run.sh` |
| `recovery-smoke` | `recovery/build.sh && recovery/tests/qemu-smoke.sh` |
| `uefi-bless` | `ci/integration/uefi-bless/run.sh` |
| `bios-bless` | `recovery/build.sh && ci/integration/bios-bless/run.sh` |
| `lifecycle-uefi`, `lifecycle-bios` | `recovery/build.sh && ci/lifecycle/packages/run.sh <uefi\|bios>` |
| `fallback-uefi`, `fallback-bios` | `recovery/build.sh && ci/recovery-fallback/run.sh <uefi\|bios>` |
| `publish-uefi`, `publish-bios` | `recovery/build.sh && ci/publish/run.sh <uefi\|bios>` |
| `candidate-uefi`, `candidate-bios` | `recovery/build.sh && ci/candidate-fallback/run.sh <uefi\|bios>` |
| `interrupt-uefi`, `interrupt-bios` | `recovery/build.sh && ci/publish-interrupt/run.sh <uefi\|bios>` |
| `graphics-uefi`, `graphics-bios` | `recovery/build.sh && ci/graphics/run.sh <uefi\|bios>` |
| `secure-boot-uefi` | `recovery/build.sh && ci/secure-boot/run.sh uefi` |

For scale: on the reference machine (i7-11800H, WSL2 with 16 CPUs and 15 GiB), with packages and the cloud image already cached, `bios-trial` takes about 1 minute, and `recovery/build.sh && ci/recovery-fallback/run.sh uefi` about 24 minutes. Most of that is boots the suite deliberately lets run until their timeout.

Each suite writes its disks, serial logs and results table under `ci/out/<suite>/`, the same files CI uploads. The Debian cloud image and `.deb` files are cached in `ci/out/cache/` and `ci/out/debs/` after the first run.

The `qemu-baseline.yml` boots are documented in [`ci/README.md`](../ci/README.md).

### VirtualBox dry runs of the hardware images

[`ci/hardware/vbox-dry-run.sh`](../ci/hardware/vbox-dry-run.sh) runs checks B1 to B4 of [`ci/hardware/README.md`](../ci/hardware/README.md) in a VirtualBox VM from WSL. It drives `VBoxManage.exe` through WSL interop and needs no root:

```bash
ci/hardware/make-usb.sh build
```

```bash
ci/hardware/vbox-dry-run.sh bios
```

```bash
ci/hardware/vbox-dry-run.sh uefi
```

Each run:
1. converts the image to a VDI under `%TEMP%\vintner-vbox\<uefi|bios>\`;
2. boots it headless in a VM named `vintner-dry-run-<uefi|bios>` (legacy BIOS or EFI firmware), with the serial port logged to `serial.log`;
3. waits for the test initramfs to power the VM off;
4. clones the disk back to raw and reads it with `make-usb.sh inspect`;
5. reports PASS or FAIL for the boot and for B2 (UEFI) or B4 (BIOS), then deletes the VM and its disks, keeping `serial.log`.

Options:
- `--keep` leaves the VM registered, so you can open it in the VirtualBox GUI.
- `VBOXMANAGE` points at another `VBoxManage.exe`.
- `VINTNER_VBOX_DIR` picks another working directory. It must be on a Windows drive, because VirtualBox does not read the WSL filesystem.

While WSL2 is enabled, VirtualBox runs its VMs on the Windows Hypervisor Platform (a turtle icon in the VM window). That is slower than its own hypervisor, but enough for these images.

## Disk space

- **Each QEMU suite leaves 10 to 30 GiB** of disk images under `ci/out/`. `ci/out/` is ignored by git.
- **Delete a suite's results** with `rm -rf ci/out/<suite>`. Keep `ci/out/cache/` and `ci/out/debs/` to avoid downloading them again.
- **WSL's virtual disk grows but does not shrink** on its own. After a large cleanup, run `wsl --manage Debian --set-sparse true` in PowerShell so freed space returns to Windows.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `/dev/kvm` missing | Enable VT-x or AMD-V in the firmware setup, add `nestedVirtualization=true` to `.wslconfig` (above), then `wsl --shutdown`. |
| `/dev/kvm: Permission denied` | Add yourself to the `kvm` group, then `wsl --shutdown`. |
| A suite stops with no output and no QEMU process using CPU (`ps -eo etime,pcpu,args \| grep -E 'qemu\|run.sh'`) | Something is waiting for input. Usually sudo wants a password: run `sudo -v` first. A tool may also be asking a question CI never sees, because CI has no terminal: look at the end of the suite's log. For example, `mcopy` without `-n` asks before overwriting a host file left by an earlier run. Put long runs under `timeout`, for example `timeout 3600 ci/publish/run.sh uefi`. |
| `losetup: ... failed to set up loop device` or `device busy` after an interrupted run | List leftovers with `losetup -a` and `findmnt \| grep ci/out`. Then `sudo umount <mountpoint>` and `sudo losetup -d /dev/loopN`. |
| Commits show up under someone else's GitHub account | Set `user.name` and `user.email` in WSL (setup step 4). |
| `vbox-dry-run.sh`: `VBoxManage.exe not found` | Install VirtualBox 7.2 on Windows, or set `VBOXMANAGE`. |
