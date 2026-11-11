# Choosing what the next boot does

`vintner next-boot` sets one request for the next boot and returns. It does not restart the machine: the caller does, for example `vintner next-boot variant safe && systemctl reboot`. WO-0010 item 4.4 adds it. Products use it for Windows' Last Known Good Configuration, for the "did not start successfully" screen's *Start Safe Mode* button and for *Return to OS Choices Menu*.

```
vintner next-boot [--json] recovery
vintner next-boot [--json] previous-good
vintner next-boot [--json] generation <n>
vintner next-boot [--json] variant [--generation <n>] <id>
vintner next-boot [--json] menu [--timeout <seconds>]
vintner next-boot [--json] clear
```

| Target | The next boot |
|---|---|
| `recovery` | boots the Recovery Console: recovery A while it is not bad, recovery B after that |
| `previous-good` | boots the newest `GOOD` generation that is older than the newest generation (Last Known Good Configuration) |
| `generation <n>` | boots generation `n`, a bad one too, like a choice from the menu |
| `variant <id>` | boots variant `id` of the generation that would boot next, the newest one that is not bad ([`variants.md`](variants.md)). That is not the running generation on its last try, which has no tries left. `--generation <n>` names the generation instead, for example `booted.generation` of `vintner status --json`: a variant has no counter, so it boots whether or not generation `n` has tries left; the generation and variant are still checked. An older vintner without the flag exits 2 |
| `menu` | shows the boot menu for `--timeout` seconds (default 30, 1 to 3600), then the first entry boots |
| `clear` | takes back a request that has not been used yet |

## Rules

- **One request, one boot.** The boot loader consumes the request when it starts, so it never outlives that boot. A new request replaces an unused earlier one. `clear` removes vintner's own request and leaves a `LoaderEntryOneShot` that is not vintner's.
- **It counts like the boot it is.** An ordinary generation chosen this way counts a try like any boot, and `vintner bless` re-arms it after `boot-complete.target`. A variant changes no boot state (D-D, [`variants.md`](variants.md)). The menu counts as the entry that is chosen in it.
- **The request is checked first.** `generation` and `variant` need the generation (and variant) in the manifest and its files on boot storage. A refused request sets nothing.
- **Health does not report it.** A `LoaderEntryOneShot` that names a vintner entry is not a finding. One that names something else still is.

## UEFI

`LoaderEntryOneShot` is matched by systemd-boot as a glob over the entry IDs (ADR-0003, finding 1):

| Target | Value |
|---|---|
| `recovery` | `vintner-recovery-*` |
| `generation <n>` | `vintner-os-<n>.efi`: the ID of a counted entry has no counter in it |
| `variant <id>` | `vintner_variant-os-<gen>-<id>.efi` |
| `menu` | `LoaderConfigTimeoutOneShot` = the seconds; no `LoaderEntryOneShot` |

`ci/selection-policy` proves the patterns on the real systemd-boot.

## BIOS

The grubenv variable `VINTNER_NEXT` holds the request: `recovery`, `<gen>`, `<gen>:<id>` or `menu:<seconds>` ([`firmware/bios/INTERFACE.md`](../firmware/bios/INTERFACE.md)). `grub.cfg` clears it in the same `save_env` as its counter write, so a power cut cannot leave a counted boot with the request still set. Where it writes no counter (variant, recovery B, menu) it saves the cleared variable first. If it cannot write the grubenv at all, it ignores the request: one that cannot be cleared would repeat on every boot. `ci/bios-trial` proves this on GRUB under SeaBIOS.

## Output and exit codes

Without `--json` the command prints one line on success and a message on stderr on failure. With `--json` it prints one JSON object on stdout, on success and on failure:

```
{"ok":true,"target":"variant","firmware":"uefi","generation":5,"variant":"safe"}
{"ok":true,"target":"menu","firmware":"bios","timeout":30}
{"ok":false,"code":"no-previous-good","error":"no GOOD generation older than the newest one (generation 3)"}
```

| Exit code | Meaning |
|---|---|
| 0 | the request is set |
| 1 | an I/O or state error (boot storage, manifest, variables); `code` is `failed` |
| 2 | a usage error; `code` is `usage` |
| 3 | the request cannot be honoured; `code` is one of `no-previous-good`, `no-such-generation`, `no-such-variant`, `no-recovery`, `nothing-bootable` |

Flags: `--boot-storage`, `--state-dir`, `--efivars` (tests and the recovery image; by default boot storage is mounted from `vintner.bootstorage=` on the kernel command line), `--json`, `--timeout`. They may come before or after the target.
