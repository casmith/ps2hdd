# Road map to 1.0

1.0 is a claim that the command surface, the configuration format and the
on-disk layout will not break under you. This is what stands between here and
being able to make that claim honestly.

Everything below is either something this program does not do, or something it
does that has never been checked against hardware. The second kind matters more
than it sounds: this project reimplements almost nothing, but it *predicts* a
great deal about what hdl_dump, pfsshell, POPStarter and OPL will do, and a
prediction read out of source is not the same as one that has run.

---

## 0. Has a PlayStation 1 game ever booted?

Nothing else on this list matters as much, and it is not a coding task.

The VCDs are written, the launchers are in `+OPL/APPS`, `DISCS.TXT` and
`VMCDIR.TXT` are in the support directories, the runtime is in `__common/POPS`.
All of it was built from reading Open PS2 Loader's and POPStarter's sources.
None of it has been confirmed on a console.

Three observations would validate the entire PlayStation 1 path:

- a PS1 game appears on OPL's **Apps** page and starts
- a multi-disc title swaps discs from inside the game
- a save made on disc 1 is there after the swap

Until then, everything under "PlayStation 1" in this repository is a careful
argument rather than a working feature.

---

## 1. Correctness that is currently assumed

### The space model — *in progress*

What a PS2 title costs is worked out by replaying hdl_dump's allocator, taken
from its C. It could never be cross-checked directly: hdl_dump accepts only
block devices, so a file-backed test needs root and a loop device.

`ps2hdd doctor` now brackets every installed partition against the model, using
the image size hdl_dump recorded and the footprint the APA table reports. That
turns an installed drive into the test. **Running it on a real drive is the
outstanding step.**

### Content verification — *not started*

`install.verify_after_install` confirms that a PS2 partition exists and that a
VCD is larger than its header. Neither reads a byte of the payload.

A library is pushed across USB to a drive in one long unattended run. A
truncated or corrupted write passes both checks today and fails on the console
much later, with nothing to point at. `ps2hdd verify` — read an installed title
back and compare it against its source — is the largest real gap in the tool,
and the one that most deserves to exist before a 1.0.

### `--demo config set` writes synthetic values to the real config

`--demo` overlays a synthetic device, source directories and artwork provider in
memory. `config set` then saves the whole overlaid configuration to the real
path, so `ps2hdd --demo config set anything x` silently replaces the device and
sources with demo ones. Small, and a trap that has already been walked into.

---

## 2. Sharp edges

- **Report the launcher name limit before a bulk run.** OPL truncates a boot
  filename at 64 bytes, so a long PS1 title gets an Apps entry that does
  nothing. Today that is one warning per install, invisible in a run of two
  hundred. The plan already walks every title and could say so up front.
- **More than one source directory per platform.** `sources.ps2` and
  `sources.ps1` are single strings.
- **Bulk operations and the install plan in the TUI.** Running `ps2hdd` with no
  arguments opens the TUI, so it is the default interface, and `--all`,
  `--from-list` and the plan exist only on the command line.

---

## 3. Housekeeping

- `v0.1.0-rc1` is published as a normal release rather than a prerelease.
- `CHANGELOG.md` links to a `v0.5.3` tag that does not exist: the release was
  drafted, never published, and its content shipped in 0.6.0.
- One archive in a 585-title library still fails identification.

---

## Deliberately not doing

- **Nested multi-part RAR.** Some collections pack a split archive inside an
  outer one. ps2hdd reports that clearly and refuses; unpacking it is a
  different program.
- **Repairing a damaged APA table.** ps2hdd refuses to initialise, format or
  repair a disk it does not recognise, and unwinding a partial write is exactly
  the kind of repair it will not attempt.

---

## A note on the demo

Three bugs in this project reached hardware because the synthetic environment
modelled something the real tools do not do: an `hdl_dump delete` verb that has
been compiled out upstream, a removal that unlinks a partition instead of
leaving `__empty` behind, and archives that were never read at all because 7z
was faked.

When the demo and the hardware disagree, the demo is the one to distrust. Each
of those was fixed by making the fake behave like the thing it stands in for,
and that is the standard for anything added to it.
