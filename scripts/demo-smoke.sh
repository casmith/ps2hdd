#!/usr/bin/env bash
#
# End-to-end smoke test of the real CLI against the synthetic environment.
#
# This is what CI runs in place of the hardware checklist: it drives the actual
# install, remove, artwork and PS1 code paths, with only the block device and
# the two external executables replaced. A failure here is a real failure.
#
#   scripts/demo-smoke.sh [path-to-ps2hdd]

set -euo pipefail

BIN="${1:-./ps2hdd}"
if [ ! -x "$BIN" ]; then
  echo "usage: $0 [path-to-ps2hdd]" >&2
  exit 2
fi
BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

export XDG_CACHE_HOME="$WORK/cache"
export XDG_CONFIG_HOME="$WORK/config"
export XDG_STATE_HOME="$WORK/state"
export XDG_RUNTIME_DIR="$WORK/run"
mkdir -p "$XDG_RUNTIME_DIR"
chmod 700 "$XDG_RUNTIME_DIR"

DEMO="$XDG_CACHE_HOME/ps2hdd/demo"
ps2hdd() { "$BIN" --demo --no-color --yes "$@"; }

fail() { echo "FAIL: $*" >&2; exit 1; }
step() { printf '\n=== %s\n' "$*"; }

step "status"
ps2hdd status | tee "$WORK/status.txt"
grep -q "APA *detected" "$WORK/status.txt" || fail "APA not detected"
grep -q "+OPL *detected" "$WORK/status.txt" || fail "+OPL not detected"

step "the synthetic library is readable"
ps2hdd list --no-artwork | tee "$WORK/list.txt"
grep -q "Burnout 3 Takedown" "$WORK/list.txt" || fail "installed PS2 title missing"
grep -q "Castlevania" "$WORK/list.txt" || fail "installed PS1 title missing"
grep -q "Final Fantasy VII" "$WORK/list.txt" || fail "multi-disc source title missing"

step "JSON output parses"
for cmd in status list "source list" "art status" doctor; do
  # doctor exits non-zero when it finds problems; its JSON must still be valid.
  "$BIN" --demo --no-color --json $cmd > "$WORK/out.json" || true
  python3 -c "import json,sys; json.load(open(sys.argv[1]))" "$WORK/out.json" \
    || fail "--json $cmd did not produce JSON"
done

step "dry run changes nothing"
ps2hdd --dry-run install "$DEMO/sources/ps2/Shadow of the Colossus.iso" | tee "$WORK/dry.txt"
grep -q "hdl_dump inject_dvd" "$WORK/dry.txt" || fail "dry run did not show the command"
ps2hdd list --installed --no-artwork > "$WORK/after-dry.txt"
grep -q "Shadow of the Colossus" "$WORK/after-dry.txt" \
  && fail "the dry run installed the game" || true

step "install a PS2 title"
ps2hdd install "$DEMO/sources/ps2/Shadow of the Colossus.iso"
ps2hdd list --installed --no-artwork > "$WORK/after-install.txt"
grep -q "Shadow of the Colossus" "$WORK/after-install.txt" \
  || fail "the PS2 title was not installed"

step "installing the same title twice is refused"
# The output is captured rather than piped: these commands exit non-zero on
# purpose, and pipefail would turn a correct refusal into a script failure.
ps2hdd install "$DEMO/sources/ps2/Shadow of the Colossus.iso" > "$WORK/dup.txt" 2>&1 || true
grep -qi "already installed" "$WORK/dup.txt" || fail "a duplicate install was not refused"

step "install a multi-disc PS1 title"
ps2hdd install \
  "$DEMO/sources/psx/Metal Gear Solid/Disc 1.cue" \
  "$DEMO/sources/psx/Metal Gear Solid/Disc 2.cue" \
  --title "Metal Gear Solid"
ps2hdd info "Metal Gear Solid" | tee "$WORK/mgs.txt"
grep -q "Discs (2)" "$WORK/mgs.txt" || fail "the PS1 title is not grouped as two discs"
grep -q "_CD1.VCD" "$WORK/mgs.txt" || fail "disc 1 VCD missing"
grep -q "_CD2.VCD" "$WORK/mgs.txt" || fail "disc 2 VCD missing"
# The two discs of this release carry different serials; that must survive.
grep -q "SLUS_005.94" "$WORK/mgs.txt" || fail "disc 1 serial lost"
grep -q "SLUS_007.76" "$WORK/mgs.txt" || fail "disc 2 serial lost"
test -f "$DEMO/partitions/pops/SLUS_005.94.Metal Gear Solid/DISCS.TXT" \
  || fail "DISCS.TXT was not written"

step "artwork sync"
ps2hdd art sync --all | tee "$WORK/art.txt"
ps2hdd art status | tee "$WORK/artstatus.txt"
grep -q "yes" "$WORK/artstatus.txt" || fail "no artwork was installed"

step "artwork is not overwritten"
COVER="$DEMO/partitions/plus-opl/ART/SLUS_210.50_COV.png"
before="$(sha256sum "$COVER" | cut -d' ' -f1)"
ps2hdd art sync --all >/dev/null
after="$(sha256sum "$COVER" | cut -d' ' -f1)"
[ "$before" = "$after" ] || fail "an existing cover was replaced without --overwrite"

step "PS1 runtime import"
mkdir -p "$WORK/pops"
printf 'user supplied' > "$WORK/pops/POPS.ELF"
printf 'user supplied' > "$WORK/pops/IOPRP252.IMG"
printf 'unrelated'     > "$WORK/pops/notes.txt"
ps2hdd setup ps1 --import "$WORK/pops" | tee "$WORK/setup.txt"
grep -q "Status: READY" "$WORK/setup.txt" || fail "PS1 support did not become ready"
grep -q "notes.txt" "$WORK/setup.txt" || fail "an unrelated file was not reported as ignored"
test ! -f "$DEMO/partitions/common/POPS/notes.txt" \
  || fail "an unrelated file was copied onto the HDD"

step "remove the PS1 title, all discs"
ps2hdd remove "Metal Gear Solid"
# --installed matters here: the title is still in the source directory, and
# a source directory is never evidence that something is installed.
ps2hdd list --ps1 --installed --no-artwork > "$WORK/after-ps1-remove.txt"
grep -q "Metal Gear Solid" "$WORK/after-ps1-remove.txt" \
  && fail "the PS1 title survived removal" || true
ls "$DEMO/partitions/pops/" > "$WORK/pops-after.txt"
grep -q "Metal Gear" "$WORK/pops-after.txt" \
  && fail "a VCD survived removal" || true

step "remove the PS2 title"
ps2hdd remove SCUS_974.72
ps2hdd list --installed --no-artwork > "$WORK/after-ps2-remove.txt"
grep -q "Shadow of the Colossus" "$WORK/after-ps2-remove.txt" \
  && fail "the PS2 title survived removal" || true

step "artwork survives a removal that did not ask to purge it"
test -f "$COVER" || fail "artwork was deleted without --purge-assets"

step "an ambiguous name is refused"
ps2hdd remove "R" > "$WORK/ambiguous.txt" 2>&1 || true
grep -q "matches" "$WORK/ambiguous.txt" || fail "an ambiguous name was not refused"
grep -q "game ID" "$WORK/ambiguous.txt" || fail "the ambiguity message is not actionable"
# Nothing may have been removed.
ps2hdd list --installed --no-artwork > "$WORK/after-ambiguous.txt"
grep -q "Ridge Racer V" "$WORK/after-ambiguous.txt" || fail "an ambiguous remove deleted a game"
grep -q "Burnout 3 Takedown" "$WORK/after-ambiguous.txt" || fail "an ambiguous remove deleted a game"

step "a kernel device name is refused"
"$BIN" --no-color --device /dev/sdb status > "$WORK/refusal.txt" 2>&1 || true
grep -q "REFUSING OPERATION" "$WORK/refusal.txt" || fail "/dev/sdb was not refused"
grep -q "stable identifier" "$WORK/refusal.txt" || fail "the refusal did not explain why"

step "the system disk is refused"
# findmnt reports btrfs subvolumes as "/dev/sda2[/@]"; strip the suffix.
root_src="$(findmnt -no SOURCE / 2>/dev/null || true)"
root_src="${root_src%%[*}"
if [ -n "$root_src" ] && [ -b "$root_src" ]; then
  root_disk="$(readlink -f "$root_src" || true)"
  by_id=""
  for link in /dev/disk/by-id/*; do
    [ -e "$link" ] || continue
    if [ "$(readlink -f "$link")" = "$root_disk" ]; then by_id="$link"; break; fi
  done
  if [ -n "$by_id" ] && [ -n "$root_disk" ]; then
    "$BIN" --no-color --device "$by_id" status > "$WORK/sysdisk.txt" 2>&1 || true
    grep -q "REFUSING OPERATION" "$WORK/sysdisk.txt" \
      || fail "the system disk was not refused"
  else
    echo "  (skipped: no by-id link for the root device)"
  fi
else
  echo "  (skipped: no device-backed root filesystem)"
fi

step "mount and unmount"
MP="$(ps2hdd mount +OPL)"
test -d "$MP/ART" || fail "the mount does not expose +OPL"
# The mount must survive the process that made it, so a shell can use it.
test -d "$MP/CFG" || fail "the mount did not persist"
ps2hdd unmount +OPL > "$WORK/unmount.txt" 2>&1 || fail "unmount failed"
grep -q "Released" "$WORK/unmount.txt" || fail "unmount said nothing"

step "unmount refuses a path it did not create"
"$BIN" --demo --no-color unmount /etc > "$WORK/badunmount.txt" 2>&1 || true
grep -q "refusing to unmount" "$WORK/badunmount.txt" \
  || fail "unmount did not refuse a path outside its own runtime directory"

step "no mounts were left behind"
if [ -d "$XDG_RUNTIME_DIR/ps2hdd" ]; then
  # Per-process directories (mnt-<pid>) must all be gone; the shared "mnt"
  # directory for persistent mounts may remain, but must be empty.
  leftovers="$(find "$XDG_RUNTIME_DIR/ps2hdd" -mindepth 1 -maxdepth 1 -type d -name 'mnt-*')"
  [ -z "$leftovers" ] || fail "per-process mount directories survived: $leftovers"
  held="$(find "$XDG_RUNTIME_DIR/ps2hdd/mnt" -mindepth 1 -maxdepth 1 2>/dev/null || true)"
  [ -z "$held" ] || fail "persistent mounts survived: $held"
fi

printf '\nAll demo smoke checks passed.\n'
