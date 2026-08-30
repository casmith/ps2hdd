# ps2hdd

A Linux-native manager for PlayStation 1 and PlayStation 2 hard drives.

Point it at the SATA disk out of your PS2 and manage the whole library from a
terminal: browse your ISOs and BIN/CUE rips, install them, remove them, and
fill in the artwork Open PS2 Loader shows on the console. No Windows, no Wine,
no OPL Manager, no WinHIIP, and no staging directory full of games you already
have somewhere else.

```
┌─────────────────────────────────────────────────────────────────────────────┐
│ ps2hdd                                     WDC WD1200JB   95.6 GiB free  READY│
│ 1 PS2 Games      Installed  filter: all                                      │
│ 2 PS1 Games                                                                  │
│ 3 Installed        SYS GAME                          ID            SIZE ARTWORK│
│ 4 Assets (2)     ○ PS1 Castlevania: SOTN            SLUS_000.67 550 MiB complete│
│ 5 Queue          ● PS2 Burnout 3: Takedown          SLUS_210.50   3.4 GiB complete│
│ 6 Drive          ○ PS2 God of War                   SCUS_973.99   7.8 GiB 2 missing│
│ 7 Settings       ○ PS1 Metal Gear Solid             SLUS_005.94   1.3 GiB 1 missing│
│                                                                              │
│                  Selected: 1   4 installed   2 missing artwork               │
│ ↑↓ move · space select · d remove · a artwork · f filter · tab view · q quit │
└─────────────────────────────────────────────────────────────────────────────┘
```

## What it manages

A PlayStation 2 internal hard drive as FreeHDBoot leaves it:

- an **APA** partition table
- PS2 games as **HDLoader** partitions
- PS1 games as **POPS** virtual CDs in `__.POPS`, launched by **POPStarter**
- artwork and per-game settings in **`+OPL`**, read by **Open PS2 Loader**

## Try it without a PS2 HDD

```sh
go build ./cmd/ps2hdd
./ps2hdd --demo
```

`--demo` builds a synthetic APA disk image and a synthetic source library under
your cache directory and runs the real program against them — the same install,
remove, artwork and PS1 code paths, with only the block device and the two
external tools replaced. It is how the test suite works, and it is a fair way
to see what the program does before pointing it at a real drive.

Every CLI command works in demo mode too:

```sh
./ps2hdd --demo list
./ps2hdd --demo install ~/.cache/ps2hdd/demo/sources/ps2/"Gran Turismo 4.iso"
./ps2hdd --demo doctor
```

## Dependencies

Only writing needs external tools. Reading the library, the partition table and
the drive status is done natively, so a missing `hdl_dump` leaves you with a
working read-only browser rather than a broken program.

| Tool | Needed for | Where |
|---|---|---|
| `lsblk` | finding disks | `util-linux`, already installed |
| `hdl_dump` | installing and removing PS2 games | [ps2homebrew/hdl-dump](https://github.com/ps2homebrew/hdl-dump) |
| `pfsfuse` | reaching `+OPL` and `__.POPS` | [ps2homebrew/pfsshell](https://github.com/ps2homebrew/pfsshell) |
| `fusermount3` | releasing those mounts | `fuse3` |

`ps2hdd doctor` tells you which are missing and what each one is for.
**`docs/dependencies.md` has per-distribution install instructions**, including
two things that are easy to get wrong: `pfsfuse` is off by default in the
pfsshell build, and it wants FUSE 2 rather than FUSE 3.

BIN/CUE to VCD conversion is built in; no `cue2pops` needed.

## Install

```sh
go build -o ps2hdd ./cmd/ps2hdd
sudo install -m 0755 ps2hdd /usr/local/bin/ps2hdd
```

Go 1.25 or later, as declared in `go.mod`.

## First run

```sh
# 1. Find the drive. Read-only; it will not touch anything.
ps2hdd detect

# 2. Save it. Only a stable /dev/disk/by-id path is ever written.
ps2hdd detect --configure

# 3. Say where your games live.
ps2hdd config set sources.ps2 /mnt/nas/games/ps2
ps2hdd config set sources.ps1 /mnt/nas/games/psx

# 4. Check everything.
ps2hdd doctor

# 5. Open the interface.
ps2hdd
```

Raw block devices are root-owned. `docs/safety.md` has a udev rule that grants
access to one specific drive, which is better than running any of this as root.

## Using the interface

`ps2hdd` with no arguments opens it. Seven views, `tab` or `1`–`7` between
them:

| View | What it does |
|---|---|
| **PS2 Games** | browse the PS2 source directory, select with `space`, install with `i` |
| **PS1 Games** | the same for PS1, with multi-disc titles shown as one game |
| **Installed** | everything on the HDD; `d` removes, `a` fetches artwork, `f` filters |
| **Assets** | artwork completeness per game; `a` syncs the selection, `A` syncs everything missing |
| **Queue** | bulk installs, with progress; leave the view and work continues |
| **Drive** | capacity, partitions, free space, PS1 readiness |
| **Settings** | source directories, artwork options, confirmations — no TOML editing |

`/` searches, `r` refreshes, `?` lists every key, `q` quits.

The interface stays responsive while scans, downloads, conversions and installs
run. Raw-HDD writes are serialised, because `hdl_dump` makes no promise about
concurrent writers to one disk.

## Using the CLI

Everything the interface does is scriptable.

```sh
ps2hdd doctor                       # check the setup
ps2hdd detect [--configure]         # find a drive (read-only)
ps2hdd status [--partitions]        # drive overview

ps2hdd source scan [--rescan]       # scan the source directories
ps2hdd source list [--ps1|--ps2]

ps2hdd list                         # the unified library
ps2hdd list --ps1 --json
ps2hdd list --missing-art
ps2hdd info SLUS_209.46
ps2hdd info ~/Downloads/game.iso    # works with no HDD at all

ps2hdd install ~/Downloads/sotc.iso
ps2hdd install "MGS (Disc 1).cue" "MGS (Disc 2).cue" --title "Metal Gear Solid"
ps2hdd install --from-source "God Hand"
ps2hdd remove SLUS_209.46
ps2hdd remove "Shadow of the Colossus" --purge-assets

ps2hdd mount +OPL                   # prints the mountpoint; stays after exit
ps2hdd unmount +OPL

ps2hdd art status [--missing]
ps2hdd art sync --all
ps2hdd assets sync SLUS_209.46 --overwrite
ps2hdd assets clean
ps2hdd database update

ps2hdd setup ps1 [--create-pops 20G] [--import ~/pops-files]
ps2hdd config show
ps2hdd config set sources.ps2 /mnt/nas/games/ps2
```

Global flags: `--device`, `--dry-run`, `--json`, `--verbose`, `--debug`,
`--yes`, `--demo`, `--no-color`, `--config`.

`--dry-run` prints the exact external command it would run, so you can see
precisely what is about to happen to the disk.

`--json` works on `status`, `list`, `info`, `detect`, `doctor`, `source list`,
`art status`, `assets status` and `setup ps1`.

## Source directories

```toml
[sources]
ps2 = "/mnt/nas/games/ps2"
ps1 = "/mnt/nas/games/psx"
```

These are places to browse, not a record of what is installed. Only the HDD
decides that.

PS2 titles are identified from `SYSTEM.CNF` inside the image, never from the
filename. PS1 rips are read the same way; `.cue` files take precedence over the
`.bin` they name, and a `.bin` referenced by a `.cue` is never listed
separately.

Multi-disc PS1 titles are grouped automatically, from either common layout:

```
Final Fantasy VII/            psx/
├── Disc 1.cue                ├── Metal Gear Solid (Disc 1).cue
├── Disc 1.bin                ├── Metal Gear Solid (Disc 1).bin
├── Disc 2.cue                ├── Metal Gear Solid (Disc 2).cue
└── Disc 2.bin                └── Metal Gear Solid (Disc 2).bin
```

Scan results are cached under `~/.cache/ps2hdd/`, keyed on path, size and
modification time, so reopening the interface does not reread every image.

You never have to put a game in a source directory: `ps2hdd install
/any/path/game.iso` works.

## Artwork

Artwork goes in `+OPL/ART` as `<serial>_<TYPE>.png` — the naming current OPL
actually looks for. `docs/compatibility.md` has the full table with sizes.

The default provider fetches front covers from the community databases PCSX2
and DuckStation use. They hold covers only; every other slot is reported as
unavailable rather than filled with a substitute. For the rest, point
`assets.mirror` at a local collection (an OPL Manager art dump, or a copy of a
working `+OPL/ART`, both work unchanged) or add URL templates for another
database. A mirror is tried before the network automatically.

**ps2hdd never overwrites artwork you already have** unless you pass
`--overwrite`. Hand-picked covers are not ours to replace.

## PlayStation 1 support

PS1 games run through POPStarter, which needs two files from Sony:

```
hdd0:__common/POPS/POPS.ELF
hdd0:__common/POPS/IOPRP252.IMG
```

**ps2hdd does not and will not ship them.** It detects whether they are there,
says so plainly, and imports copies you supply:

```sh
ps2hdd setup ps1                       # what is missing
ps2hdd setup ps1 --create-pops 20G     # create the partition the VCDs live in
ps2hdd setup ps1 --import ~/pops-files # copy in your own runtime files
```

Only files matching the documented runtime are copied; anything else in that
directory is listed and left alone.

`--create-pops` sizes the partition for the library you intend to keep — a VCD
is a raw 2352-bytes-per-sector image, so budget around 750 MB per disc, and err
large, because growing it afterwards needs a PS2-side tool. The allocation is
done by `pfsshell` rather than by ps2hdd, for the same reason installs go
through `hdl_dump`: the reference implementation decides how APA space is laid
out. ps2hdd confirms the result by reading the partition table back, because
`pfsshell` is a shell and a failed `mkpart` still exits 0.

Installing a PS1 game converts the rip to POPS's VCD format — built in, and
verified byte-for-byte against `cue2pops` v2.0 by the test suite — and copies
it into `__.POPS`. Multi-disc titles get a `DISCS.TXT` so POPStarter can swap
discs in game.

The rip must be `MODE2/2352` in a single BIN. Split dumps and 2048-byte MODE1
images are refused with an explanation rather than converted into something
that would not boot. A split dump is fixable: [BinMerger](https://github.com/israpps/BinMerger)
joins a multi-BIN cuesheet into one, which is what the error message points at.

## Raw disk safety

The tool is deliberately paranoid. It:

- refuses to persist `/dev/sdX`, which is reassigned between boots
- revalidates the device immediately before every write
- refuses any disk backing `/`, `/boot`, or any mounted Linux filesystem, with
  no override flag
- refuses when the identity is ambiguous
- never formats, initialises, resizes or repairs anything
- releases every mount it made on exit, including on Ctrl-C, and never
  unmounts one you made

A refusal looks like this and exits 3:

```
REFUSING OPERATION

Device:
  /dev/disk/by-id/nvme-eui.e8238fa6bf53...

Reason:
  /dev/nvme1n1 backs the root filesystem.
  ps2hdd will never operate on the running system's disk.

No disk modifications were made.
```

Read `docs/safety.md` before your first write. It covers the full check list,
the udev rule, and how to rehearse against a disk image.

## Files

```
~/.config/ps2hdd/config.toml     configuration
~/.cache/ps2hdd/                 scan cache, artwork cache, demo environment
~/.local/state/ps2hdd/ps2hdd.log log
$XDG_RUNTIME_DIR/ps2hdd/         mountpoints, released on exit
```

`config.example.toml` is a commented starting point. The cache is disposable.

## Troubleshooting

**"permission denied reading /dev/sdb"** — raw devices are root-owned. Install
the udev rule from `docs/safety.md`, or set `tools.sudo = true` after adding a
narrow sudoers rule.

**`detect` finds nothing** — check the drive is attached and APA-formatted.
ps2hdd will not format it for you. `ps2hdd detect --all` shows every disk and
why each was skipped.

**"pfsfuse could not find partition +OPL"** — some drives use a different OPL
partition name. `ps2hdd status --partitions` lists what is actually there.

**Artwork does not appear on the console** — check the filenames in
`+OPL/ART`. OPL wants `SLUS_209.46_COV.png`; older guides describe `BG_00` and
`SCR_00`, which current OPL does not read.

**A game will not boot after installing** — check `ps2hdd info <serial>` shows
the right media type. A DVD image installed as a CD will not boot.

**Something went wrong and you want detail** — `~/.local/state/ps2hdd/ps2hdd.log`
records device resolution, every safety check, every external command, mount
lifecycle and install stages. `--debug` mirrors it to stderr.

## Development

```sh
go test ./...          # no hardware, no external tools needed
go vet ./...
gofmt -l .
go build ./cmd/ps2hdd
```

The suite runs against synthetic APA images (`internal/apa/apasynth`),
synthetic ISO 9660 and MODE2/2352 discs (`internal/iso9660/isosynth`),
captured output from the real external tools (`testdata/`), and a full
synthetic environment (`internal/demo`) that the service-layer and interface
tests drive end to end.

Hardware tests are separate and read-only:

```sh
PS2HDD_TEST_DEVICE=/dev/disk/by-id/ata-YOUR_DRIVE go test -tags=hardware ./internal/drive/
```

The native APA reader is also checked against `hdl_dump`, which is the single
most valuable verification in the project — everything else stands on that read
path. `ps2hdd doctor` does it on every run, whenever `hdl_dump` is installed and
can read the drive:

```sh
sudo ps2hdd doctor        # see the "Reader cross-check" block
```

A disagreement is reported as a problem and doctor exits non-zero. `not checked`
means the comparison could not run — usually no `hdl_dump`, or no root — and
must not be read as a pass.

For a per-game report, a drive you have not configured, or a disk image behind a
loop device:

```sh
sudo scripts/crosscheck-hdl.sh              # the configured drive
sudo scripts/crosscheck-hdl.sh /dev/loop1   # an image; the script explains how
```

**No test in this repository writes to a block device.** Write coverage is the
manual checklist in `docs/hardware-validation.md`.

### Layout

```
cmd/ps2hdd/          entry point
internal/model/      shared types, no I/O
internal/config/     TOML configuration, XDG paths
internal/apa/        APA and HDLoader parsing (read-only, native)
internal/iso9660/    just enough ISO 9660 to read SYSTEM.CNF
internal/drive/      detection, identity, safety, mounts
internal/external/   every exec.Command in the project
internal/platform/   PS2 and PS1 inspection, VCD conversion, POPStarter
internal/catalog/    source scanning, installed reading, reconciliation
internal/asset/      artwork inventory, providers, sync
internal/app/        the service layer both front ends use
internal/cli/        Cobra commands
internal/tui/        Bubble Tea interface
internal/demo/       synthetic environment for --demo and tests
```

Core disk logic lives in the service layer, never in a Cobra `RunE` or a Bubble
Tea `Update`. Every `exec.Command` is in `internal/external`.

### Further reading

- `docs/dependencies.md` — installing `hdl_dump` and `pfsfuse`, per distribution
- `docs/compatibility.md` — every upstream fact this depends on, with sources
- `docs/safety.md` — the safety model and how to grant access properly
- `docs/hardware-validation.md` — the manual checklist

## Not supported

Reimplementing APA, PFS or HDLoader; formatting or resizing anything;
automatic partition repair; cheats; Windows.

## Licence

GPL-3.0-or-later. See `LICENSE`.

ps2hdd is not affiliated with Sony Interactive Entertainment. It bundles no
Sony code and no game data.
