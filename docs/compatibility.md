# Compatibility decisions

This file records what ps2hdd assumes about the PlayStation 2 storage stack,
where each assumption came from, and why the implementation went the way it
did. Each entry names a primary source — upstream code or current maintained
documentation — rather than a tutorial.

Verified against upstream as of August 2026.

---

## APA partition table

**Read natively, in Go. Never written.**

`internal/apa` parses the APA partition table and the HDLoader game headers
directly from the block device. Nothing in ps2hdd writes APA structures to a
real disk; every mutation goes through `hdl_dump`, which is the reference
implementation.

The on-disk layout is taken from
[`ps2homebrew/hdl-dump`](https://github.com/ps2homebrew/hdl-dump):

| Fact | Value | Source |
|---|---|---|
| Sector size | 512 bytes | `ps2_hdd.h` |
| Partition header size | 1024 bytes | `ps2_hdd.h`, `ps2_partition_header_t` |
| Magic | `"APA\0"` at offset 4 | `ps2_hdd.h`, `PS2_PARTITION_MAGIC` |
| Checksum | sum of words 1..255, stored at word 0 | `apa.c`, `apa_partition_checksum` |
| Partition list | singly linked by the `next` field, terminated by 0 | `apa.c`, `apa_slice_read` |
| Allocation unit | 128 MiB chunks | `apa.c`, `apa_setup_statistics` |
| Second slice offset | sector `0x10000000` | `apa.c`, `SLICE_2_OFFS` |
| Two-slice marker | `"APAEXT\0\0"` at header offset 500, flag bit 0 | `apa.c`, `apa_toc_read_internal` |
| Byte order | little-endian | PS2 is little-endian; `get_u32` byte-swaps only on big-endian hosts |

Partition types: `0x0001` MBR, `0x0082` swap, `0x0083` Linux, `0x0100` PFS
("game"), `0x1337` HDLoader.

**Why parse it ourselves.** `detect`, `status`, `list` and `info` are the
commands a user reaches for when something is wrong, and they need to work on a
machine where `hdl_dump` is not installed. Parsing a 1 KiB struct is also far
easier to test than shelling out. Writing APA is a different matter entirely,
and is left to the tool that has been doing it for twenty years.

**Damaged tables are reported, never repaired.** A checksum mismatch, a
partition extending past the end of a slice, or a loop in the `next` chain all
fail the read with a specific message. ps2hdd has no repair path by design.

---

## HDLoader game headers

Each `0x1337` partition holds a descriptor 1 MiB in, at partition offset
`0x101000`. Layout from `hdl.c`, `hdl_ginfo_read`:

| Offset | Field |
|---|---|
| `0x0000` | magic `0xdeadfeed` |
| `0x0008` | game name, NUL-terminated |
| `0x00a9` | compatibility flag bits |
| `0x00aa` | DMA mode word |
| `0x00ac` | startup file, e.g. `SLUS_209.46` |
| `0x00ec` | `0x14` means DVD |
| `0x00f0` | number of image extents |
| `0x00f5` | extent table, 3 × `u32` per entry |

Extent entries store the start as `start_sector >> 8` and the length in
256-byte units (`hdl.c`, `hdl_read_game_alloc_table` divides the length by 2 to
get 512-byte sectors). ps2hdd reproduces both.

A partition counts as a game when its `flags` word is exactly zero *and* its
type is `0x1337`; sub-extents share the type but carry a non-zero `flags`.

---

## hdl_dump

Command syntax, from `hdl_dump.c`'s help table:

```
hdl_toc      device [--csv]
inject_cd    target name source [startup] [+flags] [*dma] [@slice] [-hide]
inject_dvd   target name source [startup] [+flags] [*dma] [@slice] [-hide]
delete       device partition/game
cdvd_info2   iin_input [--csv]
toc          device [--dm]
```

Note that removal is spelled `delete`, not `remove`.

**`hdl_toc --csv` output.** The column header is printed *space-separated even
in CSV mode* (`show_hdl_toc` uses a separate format string for it), so the
parser skips it by content rather than by position. Rows are:

```
type;   sizeKB;flags;dma;startup;name
DVD;3538944KB;  0        ;*u4;SLUS_210.50;Burnout 3: Takedown
total 114432MB, used 15104MB, available 99328MB
```

The size field carries its own `KB` suffix, and the name is last and may itself
contain semicolons, so it is rejoined rather than indexed.

**Progress.** `hdl_dump` redraws a progress bar with carriage returns rather
than newlines, so `internal/external` splits its output on `\r` as well as
`\n`. When no percentage is present the interface shows an indeterminate
spinner; it never invents a number.

**Media type comes from the disc, not its size.** `inject_cd` and `inject_dvd`
produce different partitions, and choosing wrongly yields a game the console
will not boot.

Every PlayStation CD is a CD-ROM XA disc and carries the signature `CD-XA001`
at offset 1024 of the primary volume descriptor, in the "application used"
area; a DVD leaves those eight bytes blank. That signature is the answer, and
it is what hdl_dump uses (`isofs.c`, `isofs_detect_media_type`):

| bytes at PVD offset 1024 | media |
|---|---|
| `CD-XA001` | CD |
| eight NUL bytes | DVD |
| anything else | inconclusive |

ps2hdd follows the same rule, and falls back to a size heuristic (over 750 MiB
means DVD) only for the inconclusive case, flagging the result as a guess.

An earlier version of ps2hdd used the size heuristic alone. That was wrong in
both directions -- a mostly-empty DVD rip reads as a CD, a padded CD image
reads as a DVD -- and was caught by running `hdl_dump cdvd_info2` against the
same images and comparing. `scripts/crosscheck-hdl.sh` automates the
equivalent check for the installed-game list.

---

## PFS and pfsfuse

From the [`ps2homebrew/pfsshell`](https://github.com/ps2homebrew/pfsshell)
README:

```sh
pfsfuse --partition=+OPL /path/to/device /path/to/mountpoint
fusermount3 -u /path/to/mountpoint
```

`pfsfuse` daemonises on success, so a completed mount command means "mounted",
not "finished". `-o allow_other` is available but off by default: it exposes
the mount to every user on the machine.

`fusermount3` is preferred for unmounting, with `fusermount` accepted as a
fallback for distributions that ship only the fuse2 name.

There are two kinds of mount:

- **Ephemeral**, made for one operation (an install, an artwork sync). These
  live under `$XDG_RUNTIME_DIR/ps2hdd/mnt-<pid>/`, are reference counted so
  nested callers each get a valid path, and are always released when the
  process exits, including on a signal. The per-process directory keeps two
  concurrent runs from adopting each other's mountpoints.
- **Persistent**, made by `ps2hdd mount`. These live at a stable path under
  `$XDG_RUNTIME_DIR/ps2hdd/mnt/` and survive the process, because the point of
  that command is to hand a shell a directory it can use. `ps2hdd unmount`
  releases them and refuses any path outside that directory.

ps2hdd never unmounts something it did not create.

---

## Open PS2 Loader layout

The `+OPL` partition's root holds `ART/`, `CFG/`, `CHT/`, `THM/`, `LNG/`,
`VMC/` and `APPS/` (`Open-PS2-Loader`, `src/supportbase.c`, `basicFolders`).

Artwork paths are built as `<prefix><folder>/<startup>_<suffix>` and
configuration as `<prefix>CFG/<startup>.cfg` (`src/hddsupport.c`,
`hddGetImage`). The prefix is the root of the mounted partition when its name
starts with `+`, which `+OPL` does (`src/hddsupport.c`, `hddInitModules`).

So:

```
+OPL/ART/SLUS_209.46_COV.png
+OPL/CFG/SLUS_209.46.cfg
```

### Artwork slots and sizes

From the [OPL Manager ART quality guidelines](https://oplmanager.com/site/?docs=),
which is the specification the community art databases are built against:

| Suffix | Meaning | PS2 | PS1 |
|---|---|---|---|
| `COV` | front cover | 140×200 | 200×200 |
| `COV2` | back cover | 242×344 | 222×200 |
| `LAB` | spine | 18×240 | 12×200 |
| `BG` | background | 640×480 | 640×480 |
| `SCR`, `SCR2` | screenshots | 250×188 | 250×188 |
| `ICO` | disc icon | 64×64 | 64×64 |
| `LGO` | logo | 300×125 | 300×125 |

All PNG.

**Older documentation is wrong about this.** Guides written for earlier OPL
releases describe indexed names such as `BG_00` and `SCR_00`. Current OPL does
not look for those, and ps2hdd does not write them.

`ICO` and `LGO` are not resizable by OPL Manager and are not resized here
either; ps2hdd installs what a provider gives it without transforming images.

---

## POPStarter and PS1 titles

Layout on an APA HDD:

```
hdd0:__common/POPS/POPS.ELF          Sony, user-supplied
hdd0:__common/POPS/IOPRP252.IMG      Sony, user-supplied
hdd0:__common/POPS/POPSTARTER.ELF    POPStarter release
hdd0:__.POPS/<serial>.<title>.VCD
```

Multi-disc titles use a `_CD<n>` suffix and a `DISCS.TXT` listing the discs in
order, in a directory named after the title's base VCD name:

```
__.POPS/SLUS_005.94.Metal Gear Solid_CD1.VCD
__.POPS/SLUS_007.76.Metal Gear Solid_CD2.VCD
__.POPS/SLUS_005.94.Metal Gear Solid/DISCS.TXT
```

Filenames are capped at 89 characters, which is POPStarter's limit. ps2hdd
truncates the title while preserving the serial prefix, the disc suffix and the
extension.

**The discs of one release usually carry different serials.** Metal Gear Solid
is `SLUS_005.94` and `SLUS_007.76`. Grouping by serial would split every
multi-disc title, so grouping is by directory and base title instead, and each
disc keeps its own serial. The first disc's serial becomes the title's
identity, which is what OPL and POPStarter key artwork off.

**Archived sources are read without unpacking.** A `.7z`, `.zip` or `.rar`
holding one disc image is identified from the first 16 MiB of that image, which
covers the ISO 9660 volume descriptor and the root directory. The serial comes
from `SYSTEM.CNF` when it is reachable, and otherwise from the boot ELF in the
root directory, which every PS2 disc names for its serial. That second route is
allowed only for a partial read: with the whole image available there is no
reason to infer anything. Installing extracts to scratch space first, because
hdl_dump seeks around the image it injects and cannot read a pipe.

**Both CD sector layouts are accepted.** A DVD-based title, or a BIN converted
to ISO, is a plain 2048-byte stream; a CD-based title ripped raw is MODE2/2352,
with sync and header bytes around each block. Both are tried, 2048 first.

**A raw BIN is installed through its cuesheet.** hdl_dump's input layer cannot
read a bare MODE2/2352 `.bin` and reports `Input or output is unsupported`; the
CDRWIN cuesheet that names the `.bin` carries the sector layout and is read
without complaint. ps2hdd hands over the sheet whenever one exists, extracting
it from the archive alongside the image or finding it beside a loose one. All
54 raw rips in one 513-archive library ship a sheet.

**POPS.ELF and IOPRP252.IMG are Sony code.** ps2hdd does not ship them, cannot
ship them, and will not. It detects their absence, explains it, and imports
copies the user supplies with `ps2hdd setup ps1 --import <dir>`. Only files
whose names match the documented runtime are copied; anything else in the
import directory is listed and left alone.

**The `__.POPS` partition is created by pfsshell, not by ps2hdd.**
`ps2hdd setup ps1 --create-pops <size>` drives `pfsshell mkpart`, because APA
allocation -- a main partition plus however many sub-extents the size needs --
is the reference implementation's job, exactly as injecting a game is
hdl_dump's. pfsshell is an interactive shell, so a failed `mkpart` prints
`(!) Exit code is -1.` and then exits 0; the exit status carries no
information, and ps2hdd confirms the result by reading the partition table back
with the native reader.

---

## VCD conversion

**Implemented natively, verified byte-for-byte against cue2pops v2.0.**

A VCD is a 1 MiB POPS header followed by the raw MODE2/2352 disc stream. The
header holds a small table of contents. Layout from
[`makefu/cue2pops-linux`](https://github.com/makefu/cue2pops-linux),
`cue2pops.c`:

| Offset | Contents |
|---|---|
| `0x00` | A0 descriptor: first track type, `0xa0`, first track number, disc type `0x20` |
| `0x0a` | A1 descriptor: last track type, `0xa1`, last track number |
| `0x14` | A2 descriptor: last track type, `0xa2`, lead-out MSF in BCD |
| `0x1e` | 10 bytes per track: type, number, INDEX 00 MSF, INDEX 01 MSF, all BCD |
| `1024` | version identifier `6b 48 6e 20` |
| `1032`, `1036` | sector count, little-endian `u32`, written twice |

Timecode handling, which is what decides whether a converted game boots:

- Every track's INDEX 01 is shifted by the 2-second lead-in.
- INDEX 00 is shifted too, except on track 1 where it coincides with the start
  of the disc.
- A CDRWIN-style sheet — exactly one `PREGAP` and no `POSTGAP` — declares its
  pregap rather than including it in the image. The pregap is materialised
  during the copy, immediately before the first audio track, and every later
  timecode moves another 2 seconds.
- Sector count is `bin_size / 2352 + 150 × (pregaps + postgaps)`; the lead-out
  timecode adds a further 150 frames.

`testdata/vcd/*.header.bin` are real cue2pops v2.0 output, captured from a
build of that source, and `internal/platform/ps1` asserts against them.
A full conversion was also compared byte for byte with cue2pops during
development and matched exactly.

**Why native rather than shelling out to cue2pops.** Three reasons. Installing
a PS1 game already needs `pfsfuse`; adding a second external tool that most
distributions do not package makes the feature unreachable for most users. The
conversion is the one part of the PS1 path whose correctness is subtle, and a
native implementation can be unit tested against captured reference output. And
progress reporting through a pipe from a tool that prints one line at the end
is worse than reporting from the copy loop itself.

Set `tools.cue2pops` in the config file to a cue2pops binary to use that
instead.

**What is deliberately not implemented.** cue2pops can optionally apply
per-title cheats (`trainer`) and a PAL-to-NTSC video patch (`vmode`). ps2hdd
does neither. Silently altering a game's code is not something an install tool
should do unasked, and the plan for this project says as much about cheats.

**Input requirements.** POPS reads a raw 2352-byte-per-sector stream, so the
data track must be `MODE2/2352` and the whole disc must be in one BIN. Split
dumps (one file per track) and 2048-byte MODE1 rips are rejected with an
explanation rather than converted into an image that would not boot.

---

## Disc identification

Identity always comes from the disc, never from the filename.

- **PS2**: ISO 9660 at 2048 bytes per sector; `SYSTEM.CNF`'s `BOOT2` line.
  The reader finds the root directory through the record embedded in the volume
  descriptor at offset 156. Some tools instead navigate exclusively through the
  ISO 9660 *path table* -- hdl_dump does, and rejects an image without one as
  "bad ISOFS" -- so `internal/iso9660/isosynth` writes path tables even though
  nothing in ps2hdd reads them. Without that, an independent implementation
  cannot read the test fixtures, and cross-validation is impossible.
- **PS1**: ISO 9660 inside MODE2/2352 (user data at offset 24 of each 2352-byte
  sector); `SYSTEM.CNF`'s `BOOT` line. A 2048-byte layout is tried as a
  fallback for converted images.

Filenames are used only to derive a display title and a disc number. An image
whose `SYSTEM.CNF` cannot be read is reported as unidentified rather than
guessed at from its name.

Serials are normalised: `SLUS_209.46`, `SLUS-20946` and `slus20946` are one
identity. The OPL form is what lands on the HDD; the dashed form is what the
cover databases index by.

---

## Artwork providers

| Provider | Source | Slots |
|---|---|---|
| `ps2-covers` | [xlenore/ps2-covers](https://github.com/xlenore/ps2-covers) and [xlenore/psx-covers](https://github.com/xlenore/psx-covers) | `COV` only |
| `local` | a directory on the workstation | any |
| `http` | URL templates from the config file | any |

The xlenore collections are what PCSX2 and DuckStation ship as their default
cover sources. They are reachable over plain HTTPS with no authentication and
are indexed by the dashed serial:

```
https://raw.githubusercontent.com/xlenore/ps2-covers/main/covers/default/SLUS-20946.jpg
https://raw.githubusercontent.com/xlenore/psx-covers/main/covers/default/SLUS-00067.jpg
```

**They hold front covers only.** Every other OPL slot is reported as
unavailable rather than filled with a substitute: a background made from a
cover looks wrong on a console, and "missing" is more useful than wrong.

For the other slots, point `assets.mirror` at a local artwork collection — an
OPL Manager art dump or a copy of a working `+OPL/ART` both work unchanged —
or add `[assets.templates]` entries for another database. A mirror is chained
ahead of the remote provider automatically.

Templates understand `{serial}` (dashed), `{serial_opl}`, `{serial_plain}`,
`{type}` and `{platform}`.

---

## Install queue

Raw-HDD mutations are serialised: `hdl_dump` writes the APA table directly and
makes no promise about concurrent writers to one disk. Artwork downloads inside
an install are still concurrent, which is where the parallelism actually helps.

Progress is delivered to the queue's callback **synchronously and in order**,
with the queue's lock released. An earlier version spawned a goroutine per
notification, which meant a later percentage could overtake an earlier one, or
a "complete" could land before the stage that preceded it.

An item is announced complete **exactly once**. The operation's own progress
reporting emits a final `StageComplete`, but the queue owns terminal state and
drops it: a consumer reasonably treats "complete" as "reload the library", and
a duplicate costs a redundant read of the HDD and a redundant PFS mount.

Both properties are asserted by `TestQueueRunsInstallsInOrder`.

## Deviations from the original plan

Recorded here so they are decisions rather than drift.

- **The APA and HDL read path is native Go**, not `hdl_dump` output parsing.
  The plan says "do not reimplement APA"; nothing here writes APA, and the read
  path is what makes the tool usable without external dependencies. The
  `hdl_dump hdl_toc --csv` parser exists and is tested against fixtures, and is
  used by `doctor` as a cross-check.
- **VCD conversion is native**, for the reasons above. The plan says not to
  invent a converter if a reliable tool exists; this is not an invention but a
  reimplementation of a documented format, held to byte-for-byte equality with
  that tool by the test suite.
- **Artwork filenames follow current OPL**, not the `BG_00`/`SCR_00` scheme in
  the plan's example. The plan explicitly asked for the current rules to be
  verified rather than copied.
- **The TUI views live in `internal/tui` rather than `internal/tui/views`.**
  The reusable widgets are a real package (`internal/tui/components`); the views
  are files. Splitting the views into their own package would mean exporting the
  whole model or duplicating it, for no benefit.
