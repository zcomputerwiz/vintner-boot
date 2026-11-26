# Machine-readable status

`vintner status --json` reports the installation and the boot that is running, for products to build on. WO-0010 item 4.5 (D-F) adds it. It is the data behind Windows' "did not start successfully" screen, Last Known Good Configuration and a version list. It reads boot storage read-only, writes nothing and takes no lock, so a product can call it early in boot without waiting behind a publish.

```
vintner status [--json]
```

Flags: `--boot-storage`, `--state-dir`, `--efivars`, `--cmdline`, `--sig-enforce`, `--secureboot-dir` (tests and the recovery image; by default boot storage is mounted read-only from `vintner.bootstorage=` on the kernel command line). Without `--json` it prints a short summary for people.

## Exit codes

| Code | Meaning |
|---|---|
| 0 | the report was made. Health findings do not change it: read `healthy` and `health` |
| 1 | an I/O or state error (boot storage, manifest); with `--json`, `{"ok":false,"code":"failed","error":"…"}` |
| 2 | a usage error |
| 3 | not a vintner installation: no valid manifest; `code` is `not-vintner` |

## Fields

`format` is 1. Fields are only ever added within a format.

```
{
  "format": 1,
  "firmware": "both",            // what the installation supports: uefi, bios or both
  "booted_firmware": "uefi",     // what this boot came through
  "secure_boot_tier": "none",
  "secure_boot": false,          // enabled now; null on BIOS
  "modules": {"sig_enforce": false, "out_of_tree": "allowed", "requires_signature": null},
  "tries": 3,
  "generations": [               // newest first
    {"generation": 3, "state": "TRYING", "stale": false,
     "date": "2026-11-10", "created": "2026-11-10T12:00:00Z",
     "abi": "6.12.5-amd64", "package": "linux-image-6.12.5-amd64", "version": "6.12.5-1",
     "tries_left": 2, "first_good": "…", "last_good": "…",
     "variants": [{"id": "safe", "title": "Advanced Options"}]}
  ],
  "newest_good": 2,              // newest GOOD generation, or null
  "previous_good": 2,            // what "next-boot previous-good" boots, or null
  "recovery": [{"slot": "a", "version": "…"}, {"slot": "b", "version": "…"}],
  "booted": {
    "kind": "generation",        // generation, variant, recovery or unknown
    "generation": 3, "variant": "", "slot": "",
    "failed_attempts": 0,
    "entry": "vintner-os-3.efi",
    "is_newest_good": false,
    "fell_back": false,          // null unless kind is generation or variant
    "fell_back_from": null,
    "fell_back_attempts": null
  },
  "health": [], "healthy": true
}
```

- **`booted`** is the image this boot started. `kind` is `variant` for a variant of `generation`, `recovery` for the recovery image (`slot` `a` or `b`, no generation), and `unknown` when the boot did not come from a vintner entry (a foreign entry, or a firmware that does not say).
- **`failed_attempts`** is how many boots of this image failed since it last booted good, **not counting this boot**. It is 0 on the first try of a generation and 2 on the third, so a product shows its "did not start successfully" screen when it is above 0. The loader has counted this boot already and the bless service re-arms the counter only after `boot-complete.target`, so the number is right early in boot, before the display manager starts. It stays right after the bless service has run on UEFI, because `LoaderBootCountPath` keeps the name the entry booted with. On BIOS the counter is whole again after bless (the boot is good), so the number is 0 then. For a variant it is the count of its generation's ordinary image, because a variant counts nothing itself. It is `null` where there is no counter: recovery B, or an entry the loader did not count.
- **`is_newest_good`** is false when this boot is an older generation than the newest `GOOD` one, as after a fallback. A product compares `newest_good` with `booted.generation` for Last Known Good.
- **`fell_back`** is true when a newer generation than the booted one used up its tries without booting good, so the loader went back to this one (WO-0010 4.9). `failed_attempts` is 0 then, because the booted generation was good, so the "did not start successfully" screen reads `fell_back`, `fell_back_from` (the newest generation that failed) and `fell_back_attempts` (the tries it used, `tries` minus its counter; null when the counter is gone). A generation failed when it is not `GOOD` and its counter reads 0, or the manifest already marks it `FAILED` (reconciliation does that, and `vintner bless` runs it). A newer generation that is still `TRYING` with tries left has not failed: it is just not what booted, as when the user picks an older entry from the menu. It stays true until a generation newer than the failed one is published and boots good, and **vintner keeps no record of whether the product has shown its screen**: the product records the generation it showed it for. `fell_back` is null (and the other two null) for a recovery boot or a boot that is not a vintner entry. Fields only ever add to format 1: a vintner without them leaves a product with today's behaviour.
- **`tries_left`** is each generation's counter on boot storage: the `+<left>` of its UEFI file name, or the `x` count of its grubenv counter. 0 is bad. It is `null` where it cannot be read.
- **`modules.out_of_tree`** says whether out-of-tree modules (DKMS) can load (WO-0010 5.3, [`secure-boot.md`](secure-boot.md) §9). With Secure Boot on the kernel loads only signed modules, and the key decides. In tiers A and B Debian 13's kernel trusts the machine key (through shim's MOK list in B, through `db` in A), so it is `allowed` while DKMS is configured to sign with it (the drop-in `vintner install` wrote, naming the key and the certificate of this installation), and `blocked` when it is not (the drop-in is missing, its key does not belong to its certificate, or the certificate is not the installation's); it is `unknown` for a user who cannot read the key, which is root's. In tier `none` there is no machine key, so it is `blocked`. With Secure Boot off it follows the kernel: `blocked` when the kernel enforces module signatures, `allowed` when the parameter reads back as off, `unknown` when it cannot be read. **`modules.requires_signature`** is `true` when Secure Boot is on (only signed modules load) and `null` otherwise. `reason` says why. The text view prints it.
- **`generations[].stale`** is the manifest's flag, which only a publish recomputes. The **`health`** list therefore also has one `generation N is out of date: the module tree of <abi> changed since it was published (...)` line for the newest image of a kernel whose `/lib/modules/<abi>` no longer matches what it recorded (a DKMS build or removal; `--root` names the root to compare, default `/`). `vintner publish --kernel <abi>` publishes an image that records the current tree.
- **`health`** is the reconciliation's and the health check's findings, the lines `vintner bless` prints. A `LoaderEntryOneShot` that `vintner next-boot` set is not one ([`next-boot.md`](next-boot.md)). The ESP is not scanned for side-loaded files: that needs a mount, and `bless` does it.

## How the booted image is found

- **UEFI.** `LoaderEntrySelected` names the entry (`vintner-os-3.efi`, `vintner_variant-os-3-safe.efi`, `vintner-recovery-a.efi`). `LoaderBootCountPath` holds the counted name (`vintner-os-3+1-2.efi`: one try left, two done, this boot included), so `failed_attempts` is the done count minus one.
- **BIOS.** `grub.cfg` adds `vintner.image=<dir>` to the command line of every image it boots (`os-3`, `recovery-a`, `recovery-b`), and a variant adds its own `vintner.variant=<id>`. The kernel therefore tells the image even after the bless service cleared `VINTNER_BOOTED`. A loader payload from before the token falls back to `VINTNER_BOOTED`, which names the image until bless. `failed_attempts` is the tries minus the counter's `x` count, minus one while the boot is still unblessed. `ci/bios-trial` rows B23 to B25 prove the token on GRUB.

Existing BIOS installations need a refreshed loader payload to get the token.

## Mounting

`status` mounts boot storage read-only on demand, as `publish` mounts it read-write. The kernel cannot hold both at once, and the loser gets EBUSY. Every on-demand mount (`bootstorage.Mount`) therefore retries on EBUSY for up to five seconds before it fails, so a product that polls `status` during a kernel upgrade does not make the publish fail, and the reverse. A read-only mount (boot storage, and the ESP that `menu-timeout`, `bless` and `status` read) that meets a read-write mount held for good by something else, such as `/boot/efi` in fstab or systemd-gpt-auto's `/efi`, gets EBUSY however long it waits. It then binds that existing mount at its own mount point and remounts the bind read-only: the other mount is not touched and vintner still cannot write through its own (`bootstorage.mountReadOnly`).
