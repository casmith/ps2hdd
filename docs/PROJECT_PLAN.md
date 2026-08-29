# ps2hdd — Linux PS1/PS2 HDD Manager

## Build Directive for Claude Code

Build this project end-to-end.

Do **not** stop after scaffolding, planning, or creating placeholders. Continue implementing, testing, fixing, and refining the project until the defined v1 scope is complete or a genuine external blocker prevents further progress.

Do **not** repeatedly ask the user implementation questions that can be resolved through:

- current upstream documentation
- inspection of existing open-source tools
- reasonable engineering judgment
- test fixtures
- conservative defaults
- explicit configuration options

When multiple valid implementation choices exist, choose the safest and simplest option, document the decision, and continue.

Only stop for user input when there is a truly non-resolvable external dependency, such as:

- a required copyrighted Sony runtime file that cannot legally be bundled
- physical PS2 hardware required for a final hardware-only validation
- an inaccessible external service or repository
- an ambiguity that could risk destructive disk operations and cannot be safely resolved through detection or refusal

Otherwise, work continuously until the project is complete.

The implementation language is **Go**.

The primary user interface is a **Bubble Tea TUI**.

A full CLI must also exist underneath the TUI so every important operation can be scripted and tested independently.

---

# Project Summary

`ps2hdd` is a Linux-native PlayStation 1 / PlayStation 2 HDD management utility for systems using:

- an internal SATA HDD
- APA/PFS storage
- FreeHDBoot
- Open PS2 Loader (OPL)
- POPStarter / POPS for PS1 compatibility

The project replaces old Windows-centric workflows involving tools such as OPL Manager, WinHIIP, and ad-hoc copy procedures.

The user should be able to connect a PS2 HDD to a Linux workstation, launch:

```bash
ps2hdd
```

and manage the entire library from a terminal UI.

The same functionality must remain available through CLI commands.

---

# Primary User Experience

The ideal workflow is:

```bash
ps2hdd
```

The TUI opens and provides access to:

1. PS2 source library
2. PS1 source library
3. installed games
4. install queue
5. artwork/assets
6. HDD status
7. PS1 / POPStarter status
8. configuration

The user may point the application at arbitrary directories containing PS2 and PS1 game images.

Example configuration:

```toml
[sources]
ps2 = "/mnt/nas/games/ps2"
ps1 = "/mnt/nas/games/psx"
```

The application scans these directories and presents available games.

The source directories are **not authoritative libraries**. They are simply places the user can browse for installable images.

The HDD itself is the authoritative source of what is installed.

The user must also be able to install an individual image from anywhere:

```bash
ps2hdd install ~/Downloads/game.iso
```

No dedicated workstation-side OPL library is required.

---

# Core Goals

## Functional Goals

1. Detect APA-formatted PS2 HDDs safely.
2. Persist a stable `/dev/disk/by-id/...` identifier.
3. Show drive status and storage information.
4. List installed PS2 games.
5. List installed PS1 games.
6. Present PS1 and PS2 games in a unified library view.
7. Scan configurable source directories.
8. Identify PS1 and PS2 games from image contents where possible.
9. Install PS2 games from ISO images.
10. Install PS1 games from BIN/CUE and supported image formats.
11. Support multi-disc PS1 titles.
12. Remove PS1 and PS2 games.
13. Mount and manage PFS partitions such as `+OPL`.
14. Show missing artwork.
15. Download and install missing artwork.
16. Synchronize useful OPL metadata/assets.
17. Provide a first-class TUI.
18. Provide equivalent CLI commands.
19. Support dry-run behavior for destructive/bulk operations.
20. Remain safe around raw block devices.

---

# Non-Goals

Do not:

- reimplement APA
- reimplement PFS
- reimplement HDLoader
- require Wine
- require Windows
- require OPL Manager
- require a dedicated source-game directory
- bundle copyrighted Sony POPS files
- initialize unknown disks automatically
- resize APA partitions automatically
- silently repair potentially damaged partition tables
- guess when destructive disk identity is ambiguous

Use existing mature low-level tooling wherever practical.

---

# Technology Stack

## Language

Use:

```text
Go
```

Target the latest stable Go version that is broadly available on modern Linux distributions.

Use Go modules.

---

# TUI Stack

Primary UI:

```text
Bubble Tea
Bubbles
Lip Gloss
```

Suggested packages:

```text
github.com/charmbracelet/bubbletea
github.com/charmbracelet/bubbles
github.com/charmbracelet/lipgloss
```

The TUI must be functional over:

- a local terminal
- SSH
- typical Linux terminal emulators

Do not make mouse support mandatory.

Keyboard-first navigation is required.

---

# CLI Stack

Use Cobra unless a materially better Go-native CLI framework is identified.

Suggested:

```text
github.com/spf13/cobra
```

The TUI and CLI must share the same internal service layer.

Never place core disk logic directly inside TUI update handlers or CLI command functions.

---

# Configuration

Use TOML.

Suggested parser:

```text
github.com/pelletier/go-toml/v2
```

Use XDG directories.

Config:

```text
~/.config/ps2hdd/config.toml
```

Cache:

```text
~/.cache/ps2hdd/
```

Runtime mounts:

```text
$XDG_RUNTIME_DIR/ps2hdd/
```

Fallback if `XDG_RUNTIME_DIR` is unavailable:

```text
/tmp/ps2hdd-<uid>/
```

with strict ownership/permissions.

---

# Proposed Configuration

Example:

```toml
device = "/dev/disk/by-id/ata-WDC_WD10EZEX_..."

[sources]
ps2 = "/mnt/nas/games/ps2"
ps1 = "/mnt/nas/games/psx"

[install]
sync_assets = true
verify_after_install = true

[assets]
provider = "opl-art-db"
covers = true
backgrounds = true
screenshots = true
icons = true
logos = true

[tui]
confirm_destructive_actions = true
```

Configuration should also be editable from the TUI.

---

# Core External Tools

## hdl_dump

Use `hdl_dump` for PS2 HDLoader / APA game operations.

Responsibilities:

- list installed PS2 games
- install PS2 games
- remove PS2 games
- retrieve game/storage information where supported

Wrap the executable behind a Go interface.

Never scatter raw `exec.Command()` calls throughout the project.

---

## pfsshell / pfsfuse

Use the current `ps2homebrew/pfsshell` ecosystem.

Use `pfsfuse` to expose PFS partitions as normal filesystem trees where practical.

Likely partitions include:

```text
+OPL
__.POPS
__common
```

Example:

```text
+OPL/
├── ART/
├── CFG/
├── CHT/
├── THM/
└── VMC/
```

Mount and unmount operations must be centrally managed.

---

# External Tool Abstraction

Suggested interfaces:

```go
type HDLDump interface {
    ListGames(ctx context.Context, device string) ([]Game, error)
    Install(ctx context.Context, device string, image DiscImage) error
    Remove(ctx context.Context, device string, game Game) error
}

type PFS interface {
    Mount(ctx context.Context, device, partition, mountpoint string) error
    Unmount(ctx context.Context, mountpoint string) error
}
```

External command output parsing must be isolated and unit tested.

---

# High-Level Architecture

```text
                         ┌──────────────────┐
                         │   Core Services  │
                         │                  │
                         │ drive            │
                         │ catalog          │
                         │ installer        │
                         │ remover          │
                         │ assets           │
                         │ mounts           │
                         │ config           │
                         └────────┬─────────┘
                                  │
                   ┌──────────────┴──────────────┐
                   │                             │
             ┌─────▼─────┐                 ┌────▼─────┐
             │    CLI    │                 │    TUI   │
             │   Cobra   │                 │Bubble Tea│
             └───────────┘                 └──────────┘
```

Platform-specific behavior:

```text
Core
 |
 +-- PS2 backend
 |     |
 |     +-- ISO inspection
 |     +-- SYSTEM.CNF
 |     +-- hdl_dump
 |
 +-- PS1 backend
       |
       +-- BIN/CUE inspection
       +-- serial extraction
       +-- VCD conversion
       +-- POPStarter / POPS
```

---

# Suggested Repository Layout

```text
ps2hdd/
├── cmd/
│   └── ps2hdd/
│       └── main.go
│
├── internal/
│   ├── app/
│   │   ├── services.go
│   │   └── errors.go
│   │
│   ├── config/
│   │   ├── config.go
│   │   └── paths.go
│   │
│   ├── model/
│   │   ├── game.go
│   │   ├── disc.go
│   │   ├── drive.go
│   │   └── asset.go
│   │
│   ├── drive/
│   │   ├── detect.go
│   │   ├── safety.go
│   │   ├── identify.go
│   │   └── mounts.go
│   │
│   ├── catalog/
│   │   ├── installed.go
│   │   ├── source.go
│   │   └── reconcile.go
│   │
│   ├── platform/
│   │   ├── backend.go
│   │   ├── ps2/
│   │   │   ├── backend.go
│   │   │   ├── inspect.go
│   │   │   └── install.go
│   │   └── ps1/
│   │       ├── backend.go
│   │       ├── inspect.go
│   │       ├── convert.go
│   │       ├── pops.go
│   │       └── multidisc.go
│   │
│   ├── asset/
│   │   ├── manager.go
│   │   ├── cache.go
│   │   ├── filenames.go
│   │   └── provider/
│   │       ├── provider.go
│   │       └── opl.go
│   │
│   ├── external/
│   │   ├── command.go
│   │   ├── hdldump.go
│   │   ├── pfsfuse.go
│   │   └── converter.go
│   │
│   ├── tui/
│   │   ├── app.go
│   │   ├── keys.go
│   │   ├── styles.go
│   │   ├── views/
│   │   │   ├── sources.go
│   │   │   ├── installed.go
│   │   │   ├── assets.go
│   │   │   ├── queue.go
│   │   │   ├── drive.go
│   │   │   └── settings.go
│   │   └── components/
│   │       ├── table.go
│   │       ├── dialog.go
│   │       ├── status.go
│   │       └── progress.go
│   │
│   └── cli/
│       ├── root.go
│       ├── list.go
│       ├── install.go
│       ├── remove.go
│       ├── assets.go
│       ├── status.go
│       └── doctor.go
│
├── testdata/
├── docs/
├── scripts/
├── go.mod
├── go.sum
├── README.md
└── LICENSE
```

---

# Data Model

## Game

```go
type Platform string

const (
    PlatformPS1 Platform = "ps1"
    PlatformPS2 Platform = "ps2"
)

type Game struct {
    Platform       Platform
    Title          string
    GameID         string
    SizeBytes      int64
    StorageBackend string
    Discs          []Disc
    Assets         AssetStatus
    Installed      bool
    SourcePath     string
}
```

---

# Disc Model

```go
type Disc struct {
    Number        int
    GameID        string
    SourcePath    string
    InstalledName string
}
```

Multi-disc PS1 games must be represented as one logical title with multiple discs.

Do not assume each disc has the same serial.

---

# Source Library Scanner

The source scanner is a major v1 feature.

It must support configurable PS2 and PS1 directories.

Example:

```text
/mnt/nas/games/ps2
/mnt/nas/games/psx
```

---

# PS2 Source Scanning

Recognize at least:

```text
*.iso
```

Do not rely solely on filename.

Inspect the image when practical.

Extract:

- platform
- game serial
- title if recoverable
- media type
- size

Cache scan results so reopening the TUI does not reread every large image unnecessarily.

Invalidate cached metadata using reasonable file attributes such as:

- path
- size
- modification time

---

# PS1 Source Scanning

Support common layouts.

Example:

```text
Final Fantasy VII/
├── Disc 1.cue
├── Disc 1.bin
├── Disc 2.cue
├── Disc 2.bin
├── Disc 3.cue
└── Disc 3.bin
```

and:

```text
Castlevania - Symphony of the Night/
├── game.cue
└── game.bin
```

Treat `.cue` as the primary input when BIN/CUE is present.

Do not list `.bin` files independently when referenced by a `.cue`.

Group multi-disc titles.

Where possible derive identity from disc contents rather than directory names.

---

# Unified Catalog

The application should reconcile:

```text
Source games
+
Installed PS1 games
+
Installed PS2 games
+
Asset status
```

into a single catalog.

Possible state:

```go
type CatalogEntry struct {
    Game
    AvailableInSource bool
    Installed         bool
    MissingAssets     []AssetType
}
```

This allows the TUI to show:

```text
Available
Installed
Installed + source available
Missing assets
```

---

# TUI — Main UX

Launching:

```bash
ps2hdd
```

opens the TUI.

Suggested layout:

```text
┌─────────────────────────────────────────────────────────────────────────────┐
│ ps2hdd                                          PS2 HDD: WDC 1TB     READY │
├───────────────────┬─────────────────────────────────────────────────────────┤
│ Sources           │ Library                                                  │
│                   │                                                         │
│ > PS2 Games       │ System  Game                        ID          Status   │
│   PS1 Games       │ PS2     Burnout 3                   SLUS...     Installed│
│   Installed       │ PS2     God of War                  SCUS...     Available│
│   Assets          │ PS1     Castlevania: SOTN           SLUS...     Installed│
│   Queue           │ PS1     Metal Gear Solid            SLUS...     Missing  │
│   Drive           │                                                         │
│   Settings        │                                                         │
├───────────────────┴─────────────────────────────────────────────────────────┤
│ ↑↓ Navigate  Space Select  Enter Details  i Install  a Assets  q Quit      │
└─────────────────────────────────────────────────────────────────────────────┘
```

---

# TUI Views

## 1. PS2 Sources

Browse scanned PS2 source games.

Example:

```text
PS2 Source: /mnt/nas/games/ps2

 [ ] Ace Combat 4
 [x] Burnout 3: Takedown
 [ ] Dark Cloud 2
 [x] God Hand
 [ ] Gran Turismo 4

Selected: 2
Estimated install size: 7.8 GB
```

Actions:

```text
Space    toggle selection
Enter    details
i        install selected
/        search
r        rescan
```

---

## 2. PS1 Sources

Same general behavior.

Must show logical multi-disc titles.

Example:

```text
[ ] Castlevania: Symphony of the Night
[x] Final Fantasy VII                 3 discs
[ ] Metal Gear Solid                  2 discs
```

Details view should list discs and serials.

---

## 3. Installed

Unified PS1/PS2 listing.

Example:

```text
System  Game                         ID             Size      Assets
---------------------------------------------------------------------
PS2     Burnout 3: Takedown          SLUS_210.50    3.4 GB    ✓
PS2     God of War                   SCUS_973.99    7.8 GB    2 missing
PS1     Castlevania: SOTN            SLUS_000.67    550 MB    ✓
PS1     Metal Gear Solid             SLUS_005.94    1.3 GB    1 missing
```

Actions:

```text
Enter    game details
d        remove
a        sync assets for selected game
Space    multi-select
/        search
f        filter
```

---

## 4. Assets

Show asset completeness.

Example:

```text
Game                         Cover  BG  Screenshot  Icon  CFG
--------------------------------------------------------------
Burnout Revenge                ✓     ✗       ✗       ✓    ✓
God Hand                       ✗     ✗       ✗       ✗    ✗
Metal Gear Solid               ✓     ✓       ✗       ✓    ✓
```

Actions:

```text
a    sync selected
A    sync all missing
f    filter missing only
```

Never overwrite existing files by default.

---

## 5. Queue

Bulk install queue.

Example:

```text
Install Queue

1. Burnout 3
   ██████████████████████████████ 100% Complete

2. God Hand
   ███████████████████░░░░░░░░░░  64% Installing

3. Gran Turismo 4
                                      Waiting
```

The queue should remain responsive while installs run.

Use Bubble Tea commands/messages for progress reporting.

Do not block the UI event loop with long-running subprocesses.

---

## 6. Drive

Example:

```text
Drive

Device
/dev/disk/by-id/ata-WDC_WD10EZEX_...

Model               WDC WD10EZEX
Capacity            931.5 GB
APA                 detected
+OPL                detected
__.POPS             detected
FreeHDBoot          detected

PS2 games           42
PS1 games           19

PS2 support         READY
PS1 support         READY
```

Provide read-only partition/storage detail.

Do not expose dangerous format/resize actions casually.

---

## 7. Settings

Allow selecting:

- PS2 source directory
- PS1 source directory
- default HDD
- artwork types
- automatic asset sync after install
- confirmation behavior

A text-input/file-path workflow is acceptable.

Do not require editing TOML manually.

---

# TUI State Management

Keep UI state independent from service state.

Suggested main model:

```go
type Model struct {
    activeView View
    catalog    Catalog
    queue      InstallQueue
    drive      DriveStatus
    config     Config

    width      int
    height     int
}
```

Long operations should communicate through messages.

Examples:

```go
type InstallProgressMsg struct { ... }
type InstallCompleteMsg struct { ... }
type CatalogLoadedMsg struct { ... }
type AssetsSyncedMsg struct { ... }
type OperationErrorMsg struct { ... }
```

The TUI must remain interactive during:

- source scans
- artwork downloads
- VCD conversion
- game installation where safely possible

Only allow one raw-disk-mutating job at a time unless concurrency is proven safe.

Downloads/scans may use bounded concurrency.

---

# CLI

The TUI is primary, but everything important must also be scriptable.

Commands:

```text
ps2hdd doctor

ps2hdd detect
ps2hdd status

ps2hdd source scan
ps2hdd source list

ps2hdd list
ps2hdd info

ps2hdd install
ps2hdd remove

ps2hdd mount
ps2hdd unmount

ps2hdd art status
ps2hdd art sync

ps2hdd assets status
ps2hdd assets sync
ps2hdd assets clean

ps2hdd database update

ps2hdd setup
ps2hdd setup ps1
```

Global options:

```text
--device
--dry-run
--json
--verbose
--debug
--yes
```

---

# `ps2hdd detect`

Read-only.

Find likely PS2 HDDs.

Example:

```text
Candidate PS2 HDD

Device:
  /dev/sdb

Stable path:
  /dev/disk/by-id/ata-WDC_WD10EZEX_...

Capacity:
  931.5 GB

APA:
  detected
```

Allow:

```bash
ps2hdd detect --configure
```

to persist the stable identifier.

---

# `ps2hdd list`

Unified library.

Options:

```bash
ps2hdd list
ps2hdd list --ps1
ps2hdd list --ps2
ps2hdd list --missing-art
ps2hdd list --json
```

---

# PS2 Installation

Input:

```text
ISO
```

Flow:

```text
ISO
 |
 +--> inspect SYSTEM.CNF
 |
 +--> extract serial
 |
 +--> determine media type
 |
 +--> validate HDD
 |
 +--> hdl_dump install
 |
 +--> verify
 |
 +--> optional asset sync
```

Example:

```bash
ps2hdd install ~/Downloads/sotc.iso
```

The source image may live anywhere.

No managed staging directory is required.

---

# PS1 Installation

Typical input:

```text
CUE + BIN
```

Flow:

```text
CUE/BIN
   |
   +--> validate tracks
   |
   +--> identify PS1 serial
   |
   +--> convert to VCD
   |
   +--> validate POPStarter setup
   |
   +--> access __.POPS
   |
   +--> install VCD
   |
   +--> create required launcher/config
   |
   +--> verify
   |
   +--> optional assets
```

Research and use the current maintained Linux-compatible VCD conversion approach.

Do not invent a format converter if a reliable tool already exists.

---

# Multi-Disc PS1

Required in v1.

Input:

```bash
ps2hdd install \
  "Metal Gear Solid (Disc 1).cue" \
  "Metal Gear Solid (Disc 2).cue"
```

The source scanner should also recognize grouped games automatically.

UI:

```text
Metal Gear Solid
2 discs

Disc 1   SLUS_005.94
Disc 2   SLUS_007.76
```

Install/remove operations should preserve logical grouping.

---

# POPStarter / POPS Setup

Provide:

```bash
ps2hdd setup ps1
```

The command and TUI setup view should detect:

- `__.POPS`
- `__common`
- POPStarter launcher files
- required POPS runtime components
- required supporting files

Example:

```text
PS1 Support

__.POPS                 OK
POPSTARTER.ELF          OK
POPS runtime            MISSING
IOPRP image              MISSING

Status:
NOT READY
```

The utility may copy user-supplied runtime files.

It must **not** redistribute copyrighted Sony files.

---

# Removal

Support both platforms.

Examples:

```bash
ps2hdd remove SLUS_209.46
ps2hdd remove "Shadow of the Colossus"
```

Default behavior:

- require confirmation interactively
- no asset deletion
- verify device again immediately before write

Optional:

```bash
--yes
--dry-run
--purge-assets
```

TUI deletion must display:

- title
- platform
- game ID
- size

before confirmation.

---

# Artwork

Artwork management is a first-class feature.

## Required Operations

```bash
ps2hdd art status
ps2hdd art sync
```

TUI equivalent must support:

- selected game
- selected games
- all missing assets

Flow:

```text
Installed games
      |
      v
Game IDs
      |
      v
Inspect +OPL/ART
      |
      v
Compare expected/existing
      |
      v
Download missing only
      |
      v
Copy
      |
      v
Verify
```

Potential filenames include:

```text
GAME_ID_COV.png
GAME_ID_COV2.png
GAME_ID_BG_00.png
GAME_ID_SCR_00.png
GAME_ID_ICO.png
GAME_ID_LGO.png
```

Verify the current OPL naming rules before implementation.

Do not blindly copy old naming conventions from outdated documentation.

---

# Asset Provider System

Create a provider interface.

```go
type AssetProvider interface {
    Lookup(ctx context.Context, game Game) (AssetSet, error)
    Fetch(ctx context.Context, asset Asset) (io.ReadCloser, error)
}
```

Do not hardcode the entire project to a single repository.

Providers should support:

- remote HTTP source
- GitHub-hosted database
- local mirror
- cache
- provider changes

Use at least one currently accessible community artwork source for v1.

---

# Cache

Use:

```text
~/.cache/ps2hdd/
├── art/
├── metadata/
├── source-index/
└── downloads/
```

Source scanning should cache disc metadata.

Artwork should cache successful downloads.

Cache is disposable.

Installed state must always be discovered from the PS2 HDD.

---

# Other Assets

Support architecture for:

```text
ART
CFG
CHT
widescreen/patch metadata
```

Initial v1 may prioritize:

```text
ART
CFG
```

Do not automatically enable cheats or game-specific compatibility settings unless behavior is clearly understood.

Preserve user-authored configuration when possible.

---

# Raw Device Safety

This is a critical feature, not an implementation detail.

The tool must be paranoid.

## Stable Identity

Never persist:

```text
/dev/sdb
```

Persist:

```text
/dev/disk/by-id/...
```

---

# Required Validation Before Every Write

Immediately before any modifying command:

1. resolve configured by-id path
2. ensure path exists
3. resolve actual block device
4. verify model
5. verify serial
6. verify capacity
7. verify APA layout
8. verify expected PS2 structures
9. determine whether it backs `/`
10. determine whether it backs `/boot`
11. reject normal mounted Linux system filesystems
12. reject ambiguous device identity
13. reject changed identity from configured disk

If any safety check fails:

```text
REFUSING OPERATION
```

No override flag should casually bypass root/system-disk checks.

---

# Privilege Handling

Avoid running the entire TUI as root.

Preferred model:

```text
normal ps2hdd process
        |
        +--> privileged helper/subprocess only where required
```

Research whether:

- udev rules
- polkit
- Linux capabilities
- narrowly scoped sudo

can provide a better experience.

Do not globally chmod raw block devices.

For v1, explicit `sudo` around narrowly scoped external operations is acceptable if necessary.

---

# Mount Safety

Centralize PFS mounts.

Suggested runtime paths:

```text
$XDG_RUNTIME_DIR/ps2hdd/
├── opl/
├── pops/
└── common/
```

Mount manager responsibilities:

- detect existing mounts
- track mounts created by this process
- clean them up on exit
- respond to SIGINT/SIGTERM
- detect stale mounts
- avoid unmounting user-created mounts
- serialize conflicting operations

Implement temporary mount handling with `defer` and explicit ownership tracking.

---

# Install Queue

Bulk selection is a major TUI feature.

The user should be able to:

1. browse source games
2. mark several with Space
3. press Install
4. see a queue
5. leave the queue view while work continues
6. return to inspect progress

Suggested state:

```go
type QueueItem struct {
    Game       Game
    State      QueueState
    Progress   float64
    StatusText string
    Error      error
}
```

Possible states:

```text
waiting
inspecting
converting
installing
verifying
syncing_assets
complete
failed
cancelled
```

Raw HDD installs should be serialized unless the underlying tools explicitly guarantee safe parallel writes.

---

# Progress Parsing

If `hdl_dump` and conversion tools emit progress, parse it.

If exact progress is unavailable:

- show an indeterminate spinner
- show current stage
- never fake a percentage

Do not block the UI.

---

# Search and Filtering

TUI lists should support:

```text
/
```

for fuzzy or substring search.

Filters:

```text
PS1
PS2
installed
not installed
missing assets
multi-disc
```

Do not require sophisticated fuzzy-search dependencies if simple filtering is sufficient.

---

# Game Details View

Example:

```text
God of War

Platform        PlayStation 2
ID              SCUS_973.99
Size            7.8 GB
Installed       yes
Source          /mnt/nas/games/ps2/God of War.iso

Assets
Cover           present
Background      present
Screenshot      missing
CFG             present
```

PS1 detail should show discs.

---

# Doctor Command

Required:

```bash
ps2hdd doctor
```

Check:

```text
Go runtime/build      OK
hdl_dump              OK
pfsfuse               OK
fusermount3           OK
PS2 HDD               OK
APA                    OK
+OPL                   OK
__.POPS                OK
PS2 support            READY
PS1 support            READY / NOT READY
Source PS2 directory   OK
Source PS1 directory   OK
Asset provider         OK
```

Make failures actionable.

---

# Error Handling

Bad:

```text
exit status 1
```

Good:

```text
Unable to mount +OPL.

Device:
  /dev/disk/by-id/ata-WDC_...

Reason:
  pfsfuse could not find partition +OPL.

No disk modifications were made.
```

Use typed/sentinel errors where useful.

CLI:

```bash
--verbose
--debug
```

TUI should expose operation errors in a modal/detail pane and preserve logs.

---

# Logging

Log to an XDG state/data directory where appropriate.

Include:

- device resolution
- safety checks
- external commands
- mount lifecycle
- scan decisions
- provider lookups
- install stages

Do not log secret tokens if provider authentication is ever added.

---

# JSON Output

CLI read operations should support:

```bash
--json
```

At minimum:

```text
status
list
info
art status
assets status
doctor
```

This allows future integration with shell scripts, n8n, or other automation.

---

# NBD / Network Support — Phase 2

Modern OPL may expose the PS2 HDD using NBD.

Architect the core around a validated block-device path so it can later accept:

```text
/dev/nbd1
```

Potential future commands:

```bash
ps2hdd connect 192.168.1.50
ps2hdd disconnect
```

Then:

```bash
ps2hdd list
ps2hdd art sync
```

should work without major backend changes.

Do not make network support block v1.

---

# Testing Strategy

The project manipulates raw disks, so testing discipline is mandatory.

## Unit Tests

Test:

- config parsing
- device identity resolution
- root-disk rejection
- `lsblk` parsing
- `hdl_dump` parsing
- PFS command generation
- PS2 SYSTEM.CNF parsing
- PS1 source grouping
- CUE parsing
- game ID normalization
- asset naming
- asset comparison
- catalog reconciliation
- multi-disc modeling
- queue transitions
- provider fallback/cache behavior

---

# Fixture Tests

Capture sanitized command outputs in:

```text
testdata/
```

Examples:

```text
lsblk/
hdl_dump/
pfsshell/
pfsfuse/
system_cnf/
cue/
```

Write parsers against fixture files.

Do not require physical hardware for default tests.

---

# Fake Backends

Create interfaces early enough that the TUI can be developed and tested against fake implementations.

Example:

```text
FakeDriveService
FakeCatalogService
FakeInstaller
FakeAssetProvider
```

This allows the complete TUI to operate without a PS2 HDD during development.

Add a development/demo mode if useful:

```bash
ps2hdd --demo
```

This is optional, but highly useful if it reduces hardware coupling.

---

# Hardware Tests

Separate:

```bash
go test ./...
```

from hardware-dependent validation.

Hardware tests must never run automatically in CI.

Use build tags or explicit commands.

Example:

```bash
go test -tags=hardware ./...
```

Document exactly which tests perform writes.

---

# CI

Use GitHub Actions.

At minimum:

```text
go test ./...
go vet ./...
go build ./cmd/ps2hdd
```

Consider:

```text
staticcheck
golangci-lint
```

if configuration remains reasonable.

Do not let lint tooling dominate development.

---

# Packaging

Primary release artifact should be a standalone Linux binary.

Build:

```bash
go build -o ps2hdd ./cmd/ps2hdd
```

Install:

```bash
sudo install -m 0755 ps2hdd /usr/local/bin/ps2hdd
```

Later release targets:

```text
linux-amd64
linux-arm64
```

Optional later packaging:

```text
AUR
deb
rpm
```

Do not block v1 on distro packaging.

---

# README Requirements

README must include:

1. screenshot or terminal recording if practical
2. project purpose
3. supported PS1/PS2 storage model
4. dependencies
5. installation
6. initial setup
7. TUI usage
8. CLI examples
9. raw-disk safety warning
10. PS1 copyrighted-runtime caveat
11. source-directory configuration
12. troubleshooting
13. development/build instructions

---

# v1 CLI Surface

```text
ps2hdd

ps2hdd doctor

ps2hdd detect
ps2hdd status

ps2hdd source scan
ps2hdd source list

ps2hdd list
ps2hdd info

ps2hdd install
ps2hdd remove

ps2hdd mount
ps2hdd unmount

ps2hdd art status
ps2hdd art sync

ps2hdd assets status
ps2hdd assets sync
ps2hdd assets clean

ps2hdd database update

ps2hdd setup
ps2hdd setup ps1
```

---

# v1 TUI Scope

The TUI is **not** a future enhancement.

It is part of v1.

Required views:

```text
PS2 Sources
PS1 Sources
Installed
Assets
Queue
Drive
Settings
```

Required actions:

```text
scan source directories
search/filter games
multi-select source games
install selected
inspect installed games
delete installed games
view asset completeness
sync selected assets
sync all missing assets
view drive status
configure source paths
configure default HDD
```

---

# Development Milestones

## Milestone 1 — Repository and Core Skeleton

Implement:

- Go module
- Cobra
- Bubble Tea
- config package
- models
- service interfaces
- external command wrapper
- logging
- tests
- CI
- README skeleton

Acceptance:

```bash
go test ./...
go build ./cmd/ps2hdd
./ps2hdd --help
```

all work.

Do not stop here.

---

# Milestone 2 — Device Detection and Safety

Implement:

- `lsblk` integration
- by-id resolution
- root-device detection
- APA candidate detection
- stable configuration
- safety validation
- `detect`
- `status`

Acceptance:

The application discovers a PS2 HDD read-only and rejects unsafe targets.

Do not stop here.

---

# Milestone 3 — PS2 Installed Catalog

Implement:

- `hdl_dump` wrapper
- output parser
- installed PS2 games
- `list --ps2`
- `info`
- fixture tests

Acceptance:

Existing PS2 games are presented consistently through the service layer and CLI.

Do not stop here.

---

# Milestone 4 — Source Scanning

Implement:

- PS2 source directory scanner
- PS1 source directory scanner
- metadata cache
- CUE grouping
- multi-disc grouping
- source CLI
- catalog reconciliation

Acceptance:

Configured source directories can be scanned and presented without the TUI.

Do not stop here.

---

# Milestone 5 — Initial TUI

Implement:

- navigation shell
- PS2 Sources
- PS1 Sources
- Installed
- Drive
- Settings
- loading/error states
- search/filter
- responsive layout

Use fake services as necessary.

Acceptance:

The TUI is usable for browsing against fixtures/fakes.

Do not stop here.

---

# Milestone 6 — PS2 Install / Remove

Implement:

- ISO inspection
- SYSTEM.CNF serial extraction
- `hdl_dump` install
- verification
- removal
- confirmation
- dry run
- queue integration
- TUI progress

Acceptance:

PS2 images can be installed from arbitrary paths and removed safely.

Do not stop here.

---

# Milestone 7 — PFS / +OPL

Implement:

- `pfsfuse` wrapper
- temporary mount manager
- mount ownership
- cleanup
- `mount`
- `unmount`
- `+OPL` access

Acceptance:

`+OPL` can be safely mounted and inspected through the application.

Do not stop here.

---

# Milestone 8 — Artwork

Implement:

- provider interface
- first working provider
- cache
- OPL filename logic
- art inventory
- missing-art detection
- sync
- TUI Assets view

Acceptance:

Missing art can be displayed and populated from CLI and TUI.

Do not stop here.

---

# Milestone 9 — PS1 Discovery

Implement:

- POPStarter layout inspection
- `__.POPS` discovery
- existing PS1 game enumeration
- unified library

Acceptance:

Installed PS1 and PS2 titles appear together.

Do not stop here.

---

# Milestone 10 — PS1 Installation / Removal

Implement:

- PS1 source inspection
- CUE validation
- serial extraction
- VCD conversion wrapper
- POPStarter install flow
- verification
- removal
- TUI queue integration

Acceptance:

A normal PS1 BIN/CUE title can be installed and removed without Windows.

Do not stop here.

---

# Milestone 11 — Multi-Disc PS1

Implement:

- logical grouping
- source recognition
- install queue behavior
- installed representation
- details view
- removal behavior

Acceptance:

A multi-disc game is displayed and managed as a coherent logical title.

Do not stop here.

---

# Milestone 12 — PS1 Setup Assistant

Implement:

```bash
ps2hdd setup ps1
```

and TUI status/setup screen.

Detect missing pieces.

Support user-provided runtime files.

Do not distribute Sony binaries.

Acceptance:

The application clearly explains whether PS1 support is ready and can install legally user-supplied runtime components.

Do not stop here.

---

# Milestone 13 — Full Asset Sync

Implement:

- `assets status`
- `assets sync`
- CFG support where reliable
- preservation of user-authored files
- TUI integration

Acceptance:

Supported metadata/assets can be synchronized in addition to artwork.

Do not stop here.

---

# Milestone 14 — Polish and Hardening

Implement:

- consistent errors
- cancellation
- interrupt handling
- mount cleanup
- queue failure recovery
- docs
- JSON outputs
- tests
- CI fixes
- release build

Acceptance:

All v1 functionality works together without placeholder implementations.

---

# Definition of Done

v1 is complete when a Linux user with a supported FreeHDBoot / OPL internal SATA HDD can:

```bash
ps2hdd
```

and use the TUI to:

1. select a PS2 source directory
2. select a PS1 source directory
3. browse available PS2 titles
4. browse available PS1 titles
5. select multiple games
6. install them
7. watch install progress
8. browse installed PS1 and PS2 games
9. remove selected games
10. inspect artwork completeness
11. populate missing artwork
12. synchronize supported metadata
13. inspect HDD health/status
14. inspect PS1/POPStarter readiness

The equivalent operations must also be possible through CLI commands.

The user must **not** need:

- Windows
- Wine
- OPL Manager
- WinHIIP
- a permanent workstation OPL library
- manual art filename management

---

# Safety Invariants

These are hard requirements.

1. Never format an unknown disk automatically.
2. Never resize an APA partition automatically.
3. Never initialize a disk merely because recognition failed.
4. Never persist `/dev/sdX` as authoritative identity.
5. Never perform a write before fresh device validation.
6. Never permit normal operations against the root/system disk.
7. Never guess when drive identity is ambiguous.
8. Never delete assets by default during game removal.
9. Never overwrite artwork by default.
10. Never redistribute copyrighted Sony POPS files.
11. Never run multiple raw-HDD mutations concurrently without verified safety.
12. Never let the TUI become the only access path to core functionality.
13. Never treat source directories as installed-state truth.
14. Prefer refusal over dangerous inference.

---

# Research Requirements During Implementation

Before implementing each low-level workflow, verify current upstream behavior.

Specifically verify:

1. current `hdl_dump` CLI syntax
2. current `hdl_dump` output format
3. current `pfsfuse` syntax
4. current PFS mount requirements
5. current OPL artwork naming rules
6. current POPStarter APA-HDD layout
7. current PS1 VCD conversion tooling for Linux
8. current PS1 launcher/config requirements
9. current maintained artwork database options
10. current OPL / POPStarter expectations for PS1 artwork and metadata

Prefer:

- upstream repositories
- upstream documentation
- maintained community documentation
- source code

over stale tutorials.

Document important compatibility decisions in:

```text
docs/compatibility.md
```

---

# Instructions for Autonomous Execution

Claude Code should proceed using the following decision policy.

## When uncertain

If uncertainty is low-risk:

1. research
2. choose a reasonable implementation
3. add a test
4. document the assumption
5. continue

If uncertainty could affect disk safety:

1. choose the conservative refusal behavior
2. implement detection/reporting
3. document what is not yet safely automated
4. continue with the rest of the project

Do not stop simply because a perfect solution is unavailable.

---

# No Placeholder Completion

Do not claim completion while any required feature is still represented by:

```text
TODO
panic("not implemented")
stub return values
fake production code
disabled command
placeholder TUI view
hardcoded test-only data
```

Test fakes are fine inside tests or explicit demo mode.

Production paths must be real.

---

# Continuous Work Expectations

After each milestone:

1. run tests
2. run formatter
3. run vet/static analysis
4. fix failures
5. build the binary
6. continue to the next milestone

Do not wait for the user to tell you to continue.

Do not stop after creating a plan.

Do not stop after creating the TUI shell.

Do not stop after read-only features.

Continue until v1 is implemented to the greatest extent possible in the current environment.

---

# External Blockers

If physical hardware is unavailable, do everything possible without it:

- implement interfaces
- write parsers
- use captured/upstream-derived fixtures
- create fake backends
- test generated external commands
- document exact hardware validation procedure
- build all TUI and CLI behavior

Hardware absence is not a reason to stop the rest of development.

If copyrighted POPS runtime files are unavailable:

- implement detection
- implement user-supplied import
- implement readiness reporting
- test against fake fixtures
- document the required user action

Do not stop the rest of development.

---

# Final Deliverables

The repository should contain at least:

```text
working Go source
working TUI
working CLI
unit tests
fixture tests
CI
README
LICENSE
docs/compatibility.md
docs/safety.md
example config
build instructions
hardware validation checklist
```

Before declaring completion:

```bash
go fmt ./...
go vet ./...
go test ./...
go build ./cmd/ps2hdd
```

must succeed in the development environment, except for clearly documented external/hardware-only tests.

---

# Project Philosophy

`ps2hdd` should feel like a modern Linux package manager and library browser for a PS2 HDD.

The user should think in terms of:

```text
browse
select
install
remove
sync assets
```

not:

```text
APA partition names
PFS command syntax
HDLoader flags
POPStarter naming rules
OPL artwork filenames
Windows utilities
manual staging folders
```

Those are implementation details.

The application should hide them behind a safe, pleasant, keyboard-driven Linux interface.
