# Kernel command-line options

Desktop products need `nomodeset`, a vendor driver's options or a serial console on the kernel command line. WO-0010 item 4.2 (D-B) gives them one place to set them, `/etc/kernel/cmdline`, and a command that changes them safely. The Windows equivalent is `boot.ini`, which XP users edit from Startup and Recovery or msconfig.

## The file

`/etc/kernel/cmdline` holds the product's and the administrator's options: one or more lines, options separated by white space, blank lines and lines starting with `#` ignored. Vintner composes every image's line from three parts:

```
root=UUID=<root> ro <the file's options> vintner.bootstorage=PARTUUID=<boot storage>
```

The root and the boot storage identity come from the installation, so the file may not name them. It is refused if it holds:

- `root=…`
- `ro` or `rw` (the root is mounted read-only first)
- anything starting with `vintner.` (`vintner.bootstorage=`, `vintner.image=`, `vintner.variant=`, `vintner.recovery=` belong to vintner)
- a character that is not printable ASCII
- a composed line of more than 1900 bytes

A variant's tokens (`variants.d`, [`variants.md`](variants.md)) are added after the composed line.

## When it applies

- **`vintner install`** reads the file from the target and publishes generation 1 with it.
- **`vintner publish`** reads it at every publish. When the file exists, the new image's line is composed from it, and **nothing is carried over from the earlier image's line**: an option removed from the file is gone from the next image. An empty file means no options. A changed file counts as changed input, so the next publish makes a new generation even on the same kernel and initramfs.
- **When the file does not exist**, `publish` carries the newest `GOOD` image's line forward, as it did before the file existed.
- A file that does not validate refuses the publish with a message that names it. The images already published stay bootable.

The images that are already published keep their own line, so the previous `GOOD` image boots with the line it had (R04: nothing is overwritten in place).

## `vintner cmdline`

```
vintner cmdline [--json] show
vintner cmdline [--json] [--no-publish] set (--clear | <option>...)
```

- `show` prints the file's options and, with `--json`, the running kernel's command line.
- `set` validates the options, writes the file atomically, and publishes a new generation for the running kernel. The new generation is an ordinary `TRYING` image: it boots `tries` times, and an option that stops the boot makes the machine fall back to the previous `GOOD` image with the old line. `--clear` removes every option (the file stays, empty). `--no-publish` writes the file and leaves the publish to the next kernel or initramfs update. The options may be several arguments or one quoted string.
- If publishing fails, the file goes back to what it was, so it never differs from what was published.
- It needs the same access as `vintner publish` (root, boot storage mounted on demand) and takes the same lock.

Flags for tests and the recovery image: `--file`, `--root-dir`, `--kernel`, `--boot-storage`, `--state-dir`, `--proc-cmdline`.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | done (`show`, or `set` with the file written and, unless `--no-publish`, a generation published or found unchanged) |
| 1 | an I/O error, or the publish failed (the file is back as it was) |
| 2 | a usage error |
| 3 | refused: an option that belongs to vintner, a bad character, a line that is too long, or (for `show`) a file that does not validate |

With `--json` there is one object on stdout: `{"ok":true,"file":"…","options":["…"],"published":true,"unchanged":false,"generation":4}` for `set`, `{"ok":true,"file":"…","exists":true,"options":["…"],"running":"…"}` for `show`, and `{"ok":false,"code":"refused"|"failed","error":"…"}` on failure. Publish's progress lines go to stderr then.

### A bad option

The acceptance row is `ci/candidate-fallback` case B5: `vintner cmdline set rootfstype=vintnertestnofs` publishes generation 2, the initramfs cannot mount the root, and after three failed boots the machine is back on generation 1, whose command line does not hold the option (row C1 reads the command lines that the manifest records for generations 1 and 2, after the operation boot and after the sixth boot).

A product that finds `failed_attempts > 0` in [`vintner status --json`](status.md) can offer to `vintner cmdline set` the old options again once it is back on the previous image.
