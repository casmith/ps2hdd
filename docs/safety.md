# Raw disk safety

ps2hdd writes to a raw block device. A mistake there does not corrupt a file;
it corrupts a disk. This document describes what the program refuses to do,
what it checks before every write, and how to give it the access it needs
without handing it more than it should have.

## The invariants

These are hard requirements, enforced in code and covered by tests.

1. **Never format an unknown disk.** There is no code path that initialises a
   partition table. A disk with no APA table is reported, not fixed.
2. **Never resize an APA partition.** ps2hdd does not allocate or resize
   partitions; `hdl_dump` does, within the space it is given.
3. **Never initialise a disk because recognition failed.** A failed read is a
   failed read.
4. **Never persist `/dev/sdX`.** Kernel names are reassigned between boots and
   hotplugs. Only `/dev/disk/by-id/...` (or `by-uuid`, `by-path`,
   `by-partuuid`) is accepted in the config file, and `config set device`
   rejects anything else.
5. **Never write without fresh validation.** A write revalidates the device
   immediately beforehand, whatever an earlier read established. A disk can be
   unplugged between listing the library and modifying it.
6. **Never operate on the root or system disk.** A device backing `/`, `/boot`,
   or any mounted Linux filesystem is refused, for reads as well as writes.
   There is no override flag.
7. **Never guess when identity is ambiguous.** A by-id path that matches more
   than one whole disk, or that resolves to something lsblk does not report as
   a whole disk, is refused.
8. **Never delete artwork during removal by default.** `--purge-assets` opts in.
9. **Never overwrite artwork by default.** `--overwrite` opts in.
10. **Never redistribute Sony code.** POPS.ELF and IOPRP252.IMG are detected
    and imported from what you supply, never bundled.
11. **Never run two raw-HDD mutations at once.** The install queue is
    serialised; `hdl_dump` makes no promise about concurrent writers.
12. **Never make the TUI the only way to do something.** Every operation has a
    CLI equivalent.
13. **Never treat a source directory as installed state.** The HDD is the only
    authority on what is installed.
14. **Prefer refusal over inference.**

## What is checked before a write

`internal/drive.Validate` runs these in order. A failure produces a refusal
naming the device and the reason, and nothing is written.

1. The configured identifier is a stable one.
2. It exists.
3. It resolves to a real block device, or to a regular file (a disk image).
4. The model matches what the identifier claims.
5. The serial matches what the identifier claims.
6. The device does not back `/`.
7. The device does not back `/boot`.
8. The device carries no mounted Linux filesystem at all.
9. The capacity is readable and non-zero.
10. An APA table is present, when the operation needs one.
11. The expected PS2 structures are present.
12. The identity is unambiguous.
13. Nothing about the device contradicts the configured identifier.

The system-disk checks (6–8) run *before* the capacity read, even though the
capacity is a smaller question. Reading a capacity means opening the raw
device, which usually needs root; running it first would turn "this is the disk
your system is running from" into "permission denied". A user told they lack
permission reaches for `sudo`, and reaching for `sudo` on their system disk is
exactly what must not happen.

### When the identity cross-check cannot run

Checks 4 and 5 compare the model and serial embedded in the by-id name against
what `lsblk` reports, and are only as good as `lsblk`'s answer. util-linux
built without `libudev` cannot read the udev database and falls back to the raw
sysfs SCSI INQUIRY strings: a truncated, space-padded model and no serial at
all. A Homebrew `lsblk` ahead of `/usr/bin` on `PATH` is the usual way this
happens, and it disappears under `sudo`, whose `secure_path` excludes it.

ps2hdd treats an `lsblk` that reports no serial for a disk whose by-id name
carries one as the cross-check being *unavailable*, not as a contradiction, and
logs it. That is the same answer it gives when `lsblk` is missing altogether.
Missing evidence is not evidence of a different disk, and refusing a correct
identifier is how you teach someone to want an override flag.

### A USB-attached drive has two identifiers

An enclosure that supports ATA passthrough produces two by-id links for one
disk:

```
ata-SPCC_Solid_State_Disk_AA000000000000007834
usb-SABRENT_SSHD_AAAABBBBCCCC0003-0:0
```

The `usb-` name encodes the *bridge's* identity, while `lsblk` reports the
*drive's*, passed through by the bridge. Configure the `ata-` link — which is
what `ps2hdd detect --configure` picks. The `usb-` link is refused by check 5,
and correctly so: it names the enclosure, so moving the drive to another
enclosure would silently point it somewhere else.

On an enclosure with no passthrough there is only the `usb-` link, and it is
the right one to use. udev appends the SCSI LUN to those names (the `-0:0`
above); that is addressing rather than identity, and it is not compared.

A refusal looks like this:

```
REFUSING OPERATION

Device:
  /dev/disk/by-id/nvme-eui.e8238fa6bf530001001b444a4112fe9a

Reason:
  /dev/nvme1n1 backs the root filesystem.
  ps2hdd will never operate on the running system's disk.

No disk modifications were made.
```

It exits with status 3, distinct from an ordinary error's 1.

## Privileges

Raw block devices are root-owned. ps2hdd is built so the interface itself need
not be.

**Do not run the whole TUI as root.** It scans network shares, fetches artwork
over HTTPS, and writes to your home directory; none of that wants root.

Three options, best first.

### A udev rule (recommended)

Give a specific disk to a specific group. Create
`/etc/udev/rules.d/99-ps2hdd.rules`, substituting your drive's serial:

```
# Grant the "ps2hdd" group read/write access to one PlayStation 2 HDD.
SUBSYSTEM=="block", KERNEL=="sd*", ENV{ID_SERIAL_SHORT}=="WD-WCANM1234567", \
  GROUP="ps2hdd", MODE="0660"
```

Then:

```sh
sudo groupadd -f ps2hdd
sudo usermod -aG ps2hdd "$USER"
sudo udevadm control --reload-rules && sudo udevadm trigger
# log out and back in for the group to take effect
```

`ps2hdd detect` prints the serial of every disk it sees. This grants access to
one disk and nothing else, and it survives reboots without any setuid binary.

FUSE mounts also need `user_allow_other` in `/etc/fuse.conf` only if you pass
`-o allow_other`; ps2hdd does not by default.

### Narrowly scoped sudo

If you would rather not add a udev rule, allow only the two tools that need raw
access. In `/etc/sudoers.d/ps2hdd`:

```
# Replace "you" with your username and the paths with the real ones.
you ALL=(root) NOPASSWD: /usr/bin/hdl_dump, /usr/bin/pfsfuse
```

Then set `tools.sudo = true` in the config file. ps2hdd invokes `sudo -n`, so a
missing rule fails immediately rather than blocking a terminal interface behind
an invisible password prompt.

### Running the CLI under sudo

`sudo ps2hdd list` works and is fine for a one-off. Note that it will use
root's XDG directories, so its cache and config are separate from yours.

**What ps2hdd will not do:** `chmod` a block device, install a setuid helper,
or ask you to run the interface as root.

## Working from a disk image

A regular file is a valid device. This is how the test suite works, and it is
how to rehearse an operation safely:

```sh
sudo dd if=/dev/disk/by-id/ata-YOUR_DRIVE of=ps2.img bs=1M status=progress
ps2hdd --device ./ps2.img list
```

Images are identified by path, which is stable, so they pass check 1. They
cannot be the system disk, so checks 6–8 are trivially satisfied.

## Interrupting an operation

Ctrl-C and SIGTERM cancel the running operation and release every PFS mount the
process created for its own work. Mounts you made yourself are never touched.

A mount you asked for explicitly with `ps2hdd mount` is deliberately *not*
released on exit — you asked for it so a shell could use it. It lives under
`$XDG_RUNTIME_DIR/ps2hdd/mnt/` and `ps2hdd unmount` releases it. That command
refuses any path outside that directory, so it cannot be turned into a general
unmount tool.

**Cancelling mid-write leaves what was already written in place.** ps2hdd does
not try to unwind a partial APA write: unwinding one is exactly the kind of
repair this program refuses to attempt. A partially installed game shows up in
`ps2hdd list` and can be removed normally.

## Backups

Before your first write, take a copy of the partition table:

```sh
sudo dd if=/dev/disk/by-id/ata-YOUR_DRIVE of=ps2-toc-backup.bin bs=1M count=1
```

`hdl_dump backup_toc <device> toc.bak` does the same thing with more structure,
if you have it installed. Neither is a substitute for a full image if the games
matter to you.
