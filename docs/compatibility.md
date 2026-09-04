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

## What a title costs on the drive

Neither platform costs what its source file weighs, and both used to be
reported as if they did.

**PS2.** hdl_dump rounds the image up to whole megabytes, takes 128 MiB APA
chunks until they cover it, merges the adjacent ones pairwise, and *then*
charges overhead: 4 MiB for the main extent and 1 MiB for each sub-extent. If
the chunks already taken do not cover the image plus that overhead, it takes
another whole chunk (`apa.c`, `apa_allocate_space_in_slice`).

That last step makes the cost depend on **where** the free chunks are, not only
how many there are. The merge is a buddy merge -- two extents join only when
they are adjacent, the same size, and the pair is aligned to twice it -- so a
contiguous run collapses to two or three extents and is charged almost nothing,
while a fragmented one merges to nothing and is charged a megabyte per chunk.
The same 4.5 GB image can cost 4608 MiB on one drive and 4736 MiB on another.

ps2hdd therefore does not estimate this. The space check reads the real
partition table and replays the allocation against it
(`apa.TOC.AllocationFor`). A source listing has no drive in hand, so it shows
the worst case instead (`apa.MaxAllocationFor`), which is the figure that
cannot be an underestimate.

Measured over a 514-title library, the image sizes total 1,398.6 GiB and the
footprints 1,442.8 GiB: **3.2% more**, a median of 84 MiB per title.

**PS1.** A VCD is a 1 MiB POPS header, every track file, and any pregap the
conversion materialises for a CDRWIN-style sheet. All three terms count, and
the middle one is the trap: a Redump split dump keeps each track in its own
BIN, and reading only the file the first `FILE` line names misses the audio
entirely. On a music-heavy title that is most of the disc.

The predicted size is exact -- `demo-smoke.sh` installs a rip and compares the
prediction against the bytes on disk.

**Free space is not one number.** A PS2 title needs unallocated APA chunks. A
PS1 title needs room inside `__.POPS`, which is a partition that already
exists, so unallocated chunks are the wrong quantity in both directions: they
say yes when `__.POPS` is full, and no when `__.POPS` has room but the drive
has been fully partitioned -- which is the normal end state of a drive somebody
has finished setting up. The two are checked separately, against pfsfuse's
`statfs`, which reports PFS zone counts for real.

---

## POPStarter and PS1 titles

Layout on an APA HDD:

```
hdd0:__common/POPS/POPS.ELF          Sony, user-supplied
hdd0:__common/POPS/IOPRP252.IMG      Sony, user-supplied
hdd0:__common/POPS/POPSTARTER.ELF    POPStarter release
hdd0:__.POPS/<serial>.<title>.VCD
hdd0:+OPL/APPS/<serial>.<title>/<serial>.<title>.ELF   copy of POPSTARTER.ELF
hdd0:+OPL/APPS/<serial>.<title>/title.cfg
```

**Open PS2 Loader has no PlayStation 1 support at all.** Not a partial one, not
one that needs enabling: there is no reference to POPS, POPSTARTER or `.VCD`
anywhere in its source, and `src/hddsupport.c` knows only about HDLoader
partitions. A VCD in `__.POPS` is data. It appears in no menu, on any build.

What appears in a menu is a copy of `POPSTARTER.ELF` renamed after the VCD.
POPStarter reads its own filename to decide which VCD to mount, so the two
names must match apart from the extension. OPL finds that ELF through its Apps
page, and the rules there are exact (`src/opl.c:505,550`):

- `oplScanApps` lists `<prefix>APPS` for every enabled device. On HDD the
  prefix is the partition named by `hdd_partition` in `conf_hdd.cfg`, which OPL
  writes as `+OPL` itself (`src/hddsupport.c:148`).
- Only subdirectories are scanned. An ELF sitting loose in `APPS` is skipped.
- Each subdirectory needs a `title.cfg` giving **both** `title` and `boot`.
  Missing either drops the entry with nothing on screen to say so
  (`src/appsupport.c:202`).

Every one of those failures is silent, which is why `install` writes all three
pieces and `doctor` checks them afterwards. `ps2hdd setup ps1 --launchers`
fills in launchers for titles installed before this existed, or by another
tool.

The directory is named after the VCD rather than the title so that two releases
of one game cannot collide, and so the correspondence with `__.POPS` is visible
to anyone browsing the disk. A multi-disc title gets one launcher, pointing at
disc 1; POPStarter swaps to the rest through `DISCS.TXT`.

**OPL truncates a boot filename at 64 characters** (`APP_BOOT_MAX`), then
launches `<path>/<boot>` (`src/appsupport.c:222,447`) — so a longer name
becomes a path that does not exist, and the entry appears on the Apps page and
does nothing. POPStarter's own limit is 89, so the two really can disagree.
ps2hdd keeps the 89-character VCD name, since shortening it would put ps2hdd's
filenames at odds with every other tool, and warns instead. Those titles still
launch from wLaunchELF.

### The per-game support directory

POPStarter reads per-game files from `__common/POPS/<vcd base name>/` — **not**
from beside the VCD in `__.POPS`. Both partitions contain a POPS-shaped
directory and only one of them is read, which is a mistake with no symptom
until a disc change fails mid-game. Every disc gets its own directory.

```
__.POPS/SLUS_005.94.Metal Gear Solid_CD1.VCD
__.POPS/SLUS_007.76.Metal Gear Solid_CD2.VCD
__common/POPS/SLUS_005.94.Metal Gear Solid_CD1/DISCS.TXT
__common/POPS/SLUS_007.76.Metal Gear Solid_CD2/DISCS.TXT
__common/POPS/SLUS_007.76.Metal Gear Solid_CD2/VMCDIR.TXT
```

| File | Where | What it does |
|---|---|---|
| `DISCS.TXT` | every disc | lists every VCD in order; the disc-swap menu is built from it |
| `VMCDIR.TXT` | disc 2 onward | names disc 1's VCD, so all discs share one memory card |
| `CHEATS.TXT` | any disc | raw cheat codes and POPStarter directives |

`VMCDIR.TXT` is not optional for a multi-disc release. POPStarter gives each
VCD its own virtual memory card, so without it a save made on disc 1 is
invisible on disc 2 — a three-disc RPG that loses your save at the disc change
is installed and unplayable. It holds the *VCD filename*, not the title.

### Artwork for a PS1 title

OPL looks up an **app's** artwork by its whole boot filename, not by a serial.
`appGetItemStartup` returns the `boot` value, `appGetImage` passes it through,
and `hddGetImage` builds `<prefix>ART/<value>_<suffix>` (`src/appsupport.c`,
`src/hddsupport.c`), so the file it opens is:

```
+OPL/ART/SCUS_941.63.Final Fantasy VII_CD1.ELF_COV.png
```

Extension and all. Artwork under `SCUS_941.63_COV.png` is where OPL looks for
**games**, and a PS1 title is not one — it is an Apps entry, because OPL has no
PS1 support of its own. An Apps entry never finds it.

So each image is written twice: once under the serial, which is what a
PS1-aware frontend would want, and once under the launcher filename, which is
what OPL actually opens. Removing a title with `--purge-assets` deletes both.

The size OPL shows for a PS1 entry is the **launcher's**, not the game's — a
couple of hundred kilobytes, because that is how big a copy of `POPSTARTER.ELF`
is. It says nothing about the VCD.

### Widescreen

`ps2hdd install --widescreen` writes `$WIDESCREEN` into each disc's
`CHEATS.TXT`, turning on POPStarter's GTE widescreen hack. `install.widescreen`
in the config sets the default for every install.

It is off unless asked for, and that is deliberate: the hack corrects 3D
geometry and field of view but leaves HUDs, fonts, menus and 2D backgrounds
stretched, and some games do not run with it at all. Since it is one line in a
text file, it can be turned on or off afterwards without reinstalling.

`CHEATS.TXT` belongs to the user. ps2hdd appends to one that already exists and
never rewrites it, so hand-added cheat codes survive a reinstall; on removal it
deletes the file only when the directive it wrote is the whole content.

Filenames are capped at 89 characters, which is POPStarter's limit. ps2hdd
truncates the title while preserving the serial prefix, the disc suffix and the
extension.

**The discs of one release usually carry different serials.** Metal Gear Solid
is `SLUS_005.94` and `SLUS_007.76`. Grouping by serial would split every
multi-disc title, so grouping is by directory and base title instead, and each
disc keeps its own serial. The first disc's serial becomes the title's
identity, which is what OPL and POPStarter key artwork off.

**Archived sources are read without unpacking, on both platforms.** A `.7z`,
`.zip` or `.rar` is identified from the first 16 MiB of its disc image, which
covers the ISO 9660 volume descriptor and the root directory.

A PS1 rip is not one file the way a PS2 rip is: it is a cuesheet plus one or
more data tracks, so the cuesheet is read in full first -- it is a few hundred
bytes, and it decides whether the rip is usable at all. Identification reads
only the first track, which is where the volume descriptor lives. Installing extracts the whole
archive rather than the named member, because the `FILE` line names its track
by bare filename and the two have to land in the same directory.

An archive whose members are all archives is reported as such. Some collections
nest a multi-part RAR set inside an outer RAR; ps2hdd does not unpack those, and
says so rather than reporting "no disc image", which is true but sends the
reader looking for the wrong thing. The serial comes
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
| `opl-art` *(default)* | [Luden02/psx-ps2-opl-art-database](https://github.com/Luden02/psx-ps2-opl-art-database) | `COV` `COV2` `ICO` `LAB` `LGO` `BG` `SCR` `SCR2` |
| `ps2-covers` | [xlenore/ps2-covers](https://github.com/xlenore/ps2-covers) and [xlenore/psx-covers](https://github.com/xlenore/psx-covers) | `COV` only |
| `local` | a directory on the workstation | any |
| `http` | URL templates from the config file | any |

### opl-art

A GitHub mirror of the OPL Manager GameArt Database, indexed by the OPL serial
form and laid out one directory per game:

```
https://raw.githubusercontent.com/Luden02/psx-ps2-opl-art-database/main/PS2/SLUS_207.12/SLUS_207.12_ICO.png
```

It is the default for two reasons beyond coverage. Its images are PNG, which is
what the destination filename claims and what OPL's own guidelines specify; and
they are already at OPL's exact pixel sizes, so nothing is scaled on a console
that would rather not.

Backgrounds and screenshots are numbered in the database (`_BG_00`, `_SCR_00`)
with no unnumbered file to fall back on. OPL has room for one background and
two screenshots, so the first of each is what gets installed.

### ps2-covers

The xlenore collections are what PCSX2 and DuckStation ship as their default
cover sources, indexed by the dashed serial:

```
https://raw.githubusercontent.com/xlenore/ps2-covers/main/covers/default/SLUS-20946.jpg
https://raw.githubusercontent.com/xlenore/psx-covers/main/covers/default/SLUS-00067.jpg
```

**They hold front covers only**, as high-resolution JPEG. Every other OPL slot
is reported as unavailable rather than filled with a substitute: a background
made from a cover looks wrong on a console, and "missing" is more useful than
wrong.

### Enabling a slot nothing can supply

An enabled slot the provider cannot serve is not a gap syncing will ever close.
Those slots are reported separately from real gaps -- `ps2hdd doctor` lists
them under **Not supplied**, `art status` leaves them out of the table and names
them once underneath -- because counting them as missing artwork makes a
complete library report as permanently broken.

For slots your provider lacks, point `assets.mirror` at a local artwork
collection -- an OPL Manager art dump or a copy of a working `+OPL/ART` both
work unchanged -- or add `[assets.templates]` entries for another database. A
mirror is chained ahead of the remote provider automatically, and a chain can
fill a slot if any member can.

### Everything is written as PNG, at OPL's size

Two things have to be true of the file that lands on the HDD, and neither is
true of everything a provider serves.

It has to be a PNG. Art files are named `<serial>_<TYPE>.png` and OPL picks its
decoder from that extension, so bytes from a provider serving JPEG are
re-encoded rather than copied into a name they do not match. Anything that
cannot be decoded as an image is refused rather than written: a file the
console cannot draw is worse than an absent one, because it looks installed.

It has to be the documented size, and this is the part with teeth. OPL's slots
have exact dimensions -- 140x200 for a PS2 front cover, 64x64 for the disc --
and art that ignores them is not merely ugly. It is silently dropped.

`texLoadAll` in OPL's `src/textures.c` validates every texture before it is
drawn:

```c
static int maxSize = 720 * 512 * 4;   /* 1,474,560 bytes */

static int texSizeValidate(int width, int height, u8 psm)
{
    if (width > 1024 || height > 1024)
        return -1;
    if (gsKit_texture_size(width, height, (int)psm) > maxSize)
        return -1;
    return 0;
}
```

A texture that fails returns `ERR_BAD_DIMENSION`, is freed, and nothing is
drawn. No message reaches the screen, and the slot simply looks empty.

The bytes per pixel come from the PNG's colour type, which OPL maps to a GS
pixel format: truecolor RGB becomes `GS_PSM_CT24` at 4 bytes, palette becomes
`GS_PSM_T8` at 1. So the same image costs four times as much as a truecolor
PNG as it does paletted:

| Art | Format | Texture | Against 1,474,560 |
|---|---|---|---|
| xlenore cover, 512x736 | CT24 | 1,507,328 | **rejected, by 2%** |
| the same scaled to 140x200 | CT24 | 112,000 | fine |
| opl-art cover, 140x200 paletted | T8 | 28,000 | fine |
| disc, 64x64 paletted | T8 | 4,096 | fine |

That 2% is the whole difference between a library with covers and one without,
which is why installing scales to the slot rather than trusting the source.
Discs at 64x64 were never anywhere near the limit, so a drive could show every
disc and no cover at all and look, from the console, like a display setting was
switched off.

Whatever the source gives is scaled to the slot. A PNG already at the right
size is copied through untouched, so a database built for OPL -- `opl-art` is
one -- stays byte-identical to what it published, palette and all.

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

---

## Split rips

A Redump-style PS1 rip keeps every track in its own BIN and describes them with
one cuesheet. POPS reads a single stream, so the tracks are joined during
conversion rather than being refused.

Joining is plain concatenation in cuesheet order, with every timecode rewritten
to its absolute position: a track's start is the running sector count of the
files before it plus whatever its own sheet said. That is correct because the
two-second pregap before an audio track is real data inside that track's file,
which Redump sheets state explicitly --

```
FILE "Game (Track 2).bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
```

-- so there is no gap to synthesise and no sector to invent.

Nothing is merged onto disk first. The tracks are opened together and streamed
into the VCD in one pass, which avoids writing several hundred megabytes only
to read them straight back. A single-file rip takes the same code path with one
file in it, so the split case is not a parallel universe with its own bugs.

A track that is not a whole number of 2352-byte sectors is refused. It means a
truncated rip, and joining it anyway would shift every track after it by the
shortfall -- audio that plays from the wrong place, which is not a symptom
anybody would trace back to here.
