# Hardware validation checklist

The automated test suite runs entirely without hardware: it works against
synthetic APA images, synthetic ISO 9660 and MODE2/2352 discs, and captured
output from the real external tools. That covers parsing, safety logic, the
catalog, artwork, the VCD format and the whole interface, but it cannot prove
that a game installed by ps2hdd boots on a console.

This checklist is what closes that gap. Work through it once against a real
PS2 HDD you can afford to lose, then once against the drive you care about.

**Nothing in this file runs in CI. Nothing in it runs from `go test ./...`.**

## Before you start

- A PS2 with FreeHDBoot (or another way to launch OPL) and a HDD you can
  restore.
- A full image of that HDD, or at least the partition table:
  ```sh
  sudo dd if=/dev/disk/by-id/ata-YOUR_DRIVE of=ps2-backup.img bs=4M status=progress
  ```
- `hdl_dump` and `pfsfuse` installed; check with `ps2hdd doctor`.
- A note of the drive's serial, from `ps2hdd detect`.

Record the result of each step. A step that fails is a bug worth reporting with
the log from `~/.local/state/ps2hdd/ps2hdd.log`.

## Phase 1 — read only

No writes at all. Safe on any drive.

| # | Step | Expected |
|---|---|---|
| 1.1 | `ps2hdd detect` | The PS2 HDD appears as a candidate with APA detected. Every other disk is skipped or shows "not detected". |
| 1.2 | `ps2hdd detect --configure` | A `/dev/disk/by-id/...` path is written to the config. |
| 1.3 | `ps2hdd status` | Model, serial and capacity match the drive. `+OPL`, `__.POPS` and `__common` match what you know is there. |
| 1.4 | `ps2hdd status --partitions` | The partition list matches `hdl_dump toc <device>`. |
| 1.5 | `ps2hdd list --ps2` | Every PS2 game you have installed appears, with the right title and serial. Compare against `hdl_dump hdl_toc <device>`. |
| 1.6 | `ps2hdd list --ps1` | Every PS1 game appears; multi-disc titles appear once with the right disc count. |
| 1.7 | `ps2hdd mount +OPL` | The mountpoint contains `ART/`, `CFG/` and the rest. |
| 1.8 | `ps2hdd art status` | Matches what is actually in `+OPL/ART`. |
| 1.9 | `ps2hdd doctor` | No surprises. |
| 1.10 | Sizes | The size of a known game matches `hdl_dump hdl_toc`. |

**If 1.5 disagrees with `hdl_dump hdl_toc`, stop.** The native APA reader and
the reference implementation must agree; a disagreement means the parser is
wrong and no write should follow.

## Phase 2 — write, on a scratch drive

Use a drive you can reformat.

| # | Step | Expected |
|---|---|---|
| 2.1 | `ps2hdd --dry-run install game.iso` | Prints the exact `hdl_dump inject_*` command. Nothing changes. |
| 2.2 | `ps2hdd install game.iso` (a CD-sized PS2 title) | Completes; `inject_cd` was used (check the log). |
| 2.3 | `ps2hdd list` after 2.2 | The game appears with the right title, serial and size. |
| 2.4 | `hdl_dump hdl_toc <device>` | Agrees with 2.3. |
| 2.5 | **Boot it on the console** | OPL lists the game and it runs. |
| 2.6 | `ps2hdd install big-game.iso` (a DVD-sized PS2 title) | `inject_dvd` was used. |
| 2.7 | **Boot it on the console** | Runs. |
| 2.8 | `ps2hdd art sync --all` | Artwork appears in `+OPL/ART`. |
| 2.9 | **Check on the console** | OPL shows the covers. |
| 2.10 | `ps2hdd art sync --all` again | Nothing is overwritten; existing files are left alone. |
| 2.11 | `ps2hdd remove <serial>` | Confirmation shows title, platform, ID and size. After confirming, the game is gone from `hdl_dump hdl_toc` and the space is free. |
| 2.12 | Artwork after 2.11 | Still present: removal does not purge artwork by default. |
| 2.13 | `ps2hdd remove <serial> --purge-assets` on another title | Artwork is gone too. |

## Phase 3 — PlayStation 1

| # | Step | Expected |
|---|---|---|
| 3.1 | `ps2hdd setup ps1` | Correctly reports which runtime files are present. |
| 3.2 | `ps2hdd setup ps1 --import ~/pops` | Your own POPS.ELF and IOPRP252.IMG are copied to `__common/POPS/`; status becomes READY. |
| 3.3 | `ps2hdd install game.cue` (single disc, no CD-DA) | Completes. |
| 3.4 | The resulting VCD | `ls -l` in `__.POPS` shows header + disc size. |
| 3.5 | **Boot it on the console** | POPStarter runs the game. |
| 3.6 | `ps2hdd install game.cue` (a title **with CD-DA tracks**) | Completes. |
| 3.7 | **Boot it and listen** | Music plays at the right points. This is what the VCD table of contents is for; it is the single most valuable check in this document. |
| 3.8 | A CDRWIN-style rip (one `PREGAP`, no `POSTGAP`) | Installs; audio is still in the right place on the console. |
| 3.9 | `ps2hdd install "d1.cue" "d2.cue" --title "Some Game"` | Installs as one title, two discs. |
| 3.10 | `__.POPS/<base>/DISCS.TXT` | Lists both VCDs in order. |
| 3.11 | **Swap discs in game** | POPStarter's disc-swap combo moves between them. |
| 3.12 | `ps2hdd list --ps1` | Shows one title with 2 discs, not two titles. |
| 3.13 | `ps2hdd remove "Some Game"` | Both VCDs and the DISCS.TXT directory go. |

### Comparing against cue2pops

If you have `cue2pops` built, the strongest possible check on 3.6 is:

```sh
cue2pops "game.cue" reference.vcd
ps2hdd install "game.cue"          # then copy the VCD back off the HDD
cmp reference.vcd installed.vcd
```

They should be byte-identical. This was verified during development for
single-track, multi-track and CDRWIN-style inputs; repeating it on a title you
own is cheap and conclusive.

## Phase 4 — safety

These must all refuse. Run them; do not assume.

| # | Step | Expected |
|---|---|---|
| 4.1 | `ps2hdd --device /dev/sdb status` | `REFUSING OPERATION`, unstable identifier. |
| 4.2 | `ps2hdd --device <by-id of your system disk> status` | `REFUSING OPERATION`, backs the root filesystem. |
| 4.3 | `ps2hdd --device <by-id of a blank USB stick> list` | `REFUSING OPERATION`, no APA table; the stick is untouched. |
| 4.4 | Unplug the HDD, then `ps2hdd list` | Refuses; does not fall back to another disk. |
| 4.5 | Ctrl-C during an install | Stops. `$XDG_RUNTIME_DIR/ps2hdd/` has no leftover mount directory. `mount \| grep pfs` is clean. |
| 4.6 | A partial install from 4.5 | Appears in `ps2hdd list` and can be removed. |
| 4.7 | Fill the drive, then install one more | Refuses with a space message before writing anything. |

## Phase 5 — the interface

| # | Step | Expected |
|---|---|---|
| 5.1 | `ps2hdd` over SSH | Renders; keyboard navigation works with no mouse. |
| 5.2 | Resize the terminal, including below 60 columns | Layout adapts; nothing wraps or overflows. |
| 5.3 | Select several games with space, press `i` | Confirmation shows the count and total size. |
| 5.4 | Leave the Queue view mid-install | The install continues; the sidebar badge counts down. |
| 5.5 | Return to Queue | Progress is current. |
| 5.6 | `d` on an installed game | Confirmation names title, platform, ID and size, with **No** selected. |
| 5.7 | Press Enter on that dialog | Nothing is removed. |
| 5.8 | Settings, change a source directory, `s` | Saved to the config file; a rescan picks it up. |
| 5.9 | `q` with a queue running | Asks before cancelling. |

## Recording a result

```
Date:
Drive:            model, capacity, serial
PS2 model:
OPL version:
POPStarter:
ps2hdd version:   ps2hdd --version
hdl_dump version:
pfsfuse version:

Phase 1: pass/fail, notes
Phase 2: ...
```

Attach `~/.local/state/ps2hdd/ps2hdd.log` to anything you report.

## Hardware-only automated tests

`internal/drive/hardware_test.go` carries a small set of read-only checks
behind a build tag. They never run by default:

```sh
PS2HDD_TEST_DEVICE=/dev/disk/by-id/ata-YOUR_DRIVE go test -tags=hardware ./internal/drive/
```

They are read-only. **No test in this repository, tagged or not, writes to a
block device.** Writes are covered by the manual checklist above, because a
test that writes to a disk is a test that can destroy one.
