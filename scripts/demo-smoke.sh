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
# And on the PS1 side, which reads a different library to answer the same
# question -- a second copy of a game would otherwise fill __.POPS.
ps2hdd install "$DEMO/sources/psx/Castlevania - Symphony of the Night/Castlevania - Symphony of the Night.cue" \
  > "$WORK/dup-ps1.txt" 2>&1 || true
grep -qi "already installed" "$WORK/dup-ps1.txt" \
  || fail "a duplicate PS1 install was not refused: $(cat "$WORK/dup-ps1.txt")"

step "--all plans the whole library without writing"
ps2hdd install --all --dry-run > "$WORK/plan.txt" 2>&1
grep -q "Would install" "$WORK/plan.txt" || fail "the bulk plan reported nothing"
grep -q "PlayStation 2" "$WORK/plan.txt" || fail "the plan omitted PS2"
grep -q "PlayStation 1" "$WORK/plan.txt" || fail "the plan omitted PS1"
# The two platforms consume different pools and the plan has to say which.
grep -q "APA partition table" "$WORK/plan.txt" || fail "the plan does not name the PS2 pool"
grep -q "__.POPS" "$WORK/plan.txt" || fail "the plan does not name the PS1 pool"
# Planning writes nothing.
before="$(ls "$DEMO/partitions/pops/" | wc -l)"
ps2hdd install --all --ps1 --dry-run > "$WORK/plan-ps1.txt"
grep -q "PlayStation 2" "$WORK/plan-ps1.txt" && fail "--ps1 planned PS2 titles as well" || true
[ "$(ls "$DEMO/partitions/pops/" | wc -l)" = "$before" ] || fail "a dry-run plan wrote to the HDD"

step "--from-list plans a chosen subset"
cat > "$WORK/wanted.txt" <<'LIST'
# what I actually want
Gran Turismo 4
SCUS_974.72     # by serial, with a note
Metal Gear Solid

Castlevania
LIST
ps2hdd install --from-list "$WORK/wanted.txt" --dry-run > "$WORK/list-plan.txt" 2>&1
# Two of the four are already on the drive by this point -- Castlevania ships
# installed and Shadow of the Colossus went on above -- so they are skipped
# rather than counted against the free space a second time.
grep -q "Would install 2 title" "$WORK/list-plan.txt" \
  || fail "the list plan did not cover two titles: $(cat "$WORK/list-plan.txt")"
grep -q "2 already installed" "$WORK/list-plan.txt" \
  || fail "installed titles were counted into the plan"
grep -q "PlayStation 2" "$WORK/list-plan.txt" || fail "the list plan omitted PS2"
grep -q "PlayStation 1" "$WORK/list-plan.txt" || fail "the list plan omitted PS1"
# A list is very often a directory listing, so filenames have to resolve too --
# they match no title (the extension is not part of one) and look like no path.
(cd "$DEMO/sources/ps2" && ls *.iso) > "$WORK/by-filename.txt"
ps2hdd install --from-list "$WORK/by-filename.txt" --ps2 --dry-run > "$WORK/fn-plan.txt" 2>&1
grep -q "could not be resolved" "$WORK/fn-plan.txt" \
  && fail "a directory listing did not resolve: $(cat "$WORK/fn-plan.txt")" || true
grep -q "PlayStation 2" "$WORK/fn-plan.txt" || fail "the filename plan covered nothing"
# A line that resolves to nothing stops the whole plan, by line number: a typo
# that silently dropped one game from a long list would be found much later.
printf 'Gran Turismo 4\nGrand Turismo 5\n' > "$WORK/typo.txt"
ps2hdd install --from-list "$WORK/typo.txt" --dry-run > "$WORK/typo-out.txt" 2>&1 \
  && fail "a list with an unresolvable entry was planned anyway" || true
grep -q "line 2" "$WORK/typo-out.txt" || fail "the failure does not name the line"

step "--all installs what the plan said fits"
# A batch of several hundred titles will meet a bad one; what matters is that
# the run carries on past it. The synthetic environment cannot produce a
# failing install on its own -- the fake hdl_dump never reads the source, so
# corrupting one changes nothing -- so one is injected.
ps2hdd install --all --ps2 > "$WORK/batch.txt" 2>&1 || true
grep -q "Installed" "$WORK/batch.txt" || fail "the batch installed nothing: $(cat "$WORK/batch.txt")"
ps2hdd list --ps2 --installed --no-artwork > "$WORK/after-batch.txt"
grep -q "Gran Turismo 4" "$WORK/after-batch.txt" || fail "a planned title was not installed"

step "a PS2 batch never mounts the PS1 partition"
# Game.Key is prefixed with the platform, so a PS2 title cannot match a PS1
# entry -- and reading the PS1 library means a pfsfuse mount of __.POPS. That
# mount used to happen once per title, which is what this guards against
# coming back: the count must not grow with the batch.
for g in "Gran Turismo 4" "Ridge Racer V" "Shadow of the Colossus" "Burnout 3 Takedown"; do
  ps2hdd remove "$g" >/dev/null 2>&1 || true
done
"$BIN" --demo --no-color --yes --debug install --all --ps2 > "$WORK/mounts.txt" 2>&1 || true
pops=$(grep -c 'mounted PFS partition.*__\.POPS' "$WORK/mounts.txt" || true)
# One mount and one unmount for the catalog build, and nothing per title.
[ "$pops" -le 2 ] \
  || fail "__.POPS was mounted $pops times during a PS2-only batch; it should not scale with the batch"

step "a failing title does not stop the batch"
ps2hdd remove "Gran Turismo 4"
ps2hdd remove "Ridge Racer V"
PS2HDD_DEMO_FAIL="Gran Turismo" ps2hdd install --all --ps2 > "$WORK/batch-fail.txt" 2>&1 \
  && fail "a batch with a failing title reported success" || true
grep -q "Gran Turismo 4" "$WORK/batch-fail.txt" || fail "the failing title was not named"
ps2hdd list --ps2 --installed --no-artwork > "$WORK/after-fail.txt"
grep -q "Ridge Racer V" "$WORK/after-fail.txt" \
  || fail "the run stopped at the failure instead of carrying on"
grep -q "Gran Turismo 4" "$WORK/after-fail.txt" \
  && fail "the failing title was recorded as installed" || true

step "re-running picks up what failed"
# There is no resume state: a re-run skips what is already on the drive, which
# is the same answer a saved position would give and cannot go stale.
ps2hdd install --all --ps2 > "$WORK/batch-resume.txt" 2>&1
grep -q "Installed  1 of 1" "$WORK/batch-resume.txt" \
  || fail "the re-run did not install exactly the one that failed: $(cat "$WORK/batch-resume.txt")"

step "a batch over archives unpacks the next while writing the current"
# 7z runs for real in the demo -- it only reads the source library and writes
# into scratch -- so the archive paths are exercised rather than faked.
(cd "$DEMO/sources/ps2" && for f in *.iso; do
  7z a -mx=0 -bso0 -bsp0 "${f%.iso}.7z" "$f" >/dev/null && rm "$f"
done)
# Everything is on the drive by now, so the titles are taken back off to give
# the pipeline something to do. Re-installing them from archives is the point:
# these are the same images, repacked.
for g in "Gran Turismo 4" "Ridge Racer V" "Shadow of the Colossus" "Burnout 3 Takedown"; do
  ps2hdd remove "$g" >/dev/null 2>&1 || true
done
ps2hdd install --all --ps2 > "$WORK/pipelined.txt" 2>&1 || true
grep -q "Unpacked ahead" "$WORK/pipelined.txt" \
  || fail "nothing was unpacked ahead: $(cat "$WORK/pipelined.txt")"
# Every unpacked copy is a duplicate of data still in the archive; none may
# outlive the run.
test -z "$(ls -A "$XDG_CACHE_HOME/ps2hdd/scratch" 2>/dev/null)" \
  || fail "scratch survived a pipelined batch: $(ls -A "$XDG_CACHE_HOME/ps2hdd/scratch")"
ps2hdd list --ps2 --installed --no-artwork > "$WORK/after-pipelined.txt"
grep -q "Gran Turismo 4" "$WORK/after-pipelined.txt" || fail "a title from an archive was not installed"

step "prefetch can be turned off"
ps2hdd remove "Gran Turismo 4"
ps2hdd config set install.prefetch 1
ps2hdd install --all --ps2 > "$WORK/serial.txt" 2>&1 || true
grep -q "Unpacked ahead" "$WORK/serial.txt" && fail "prefetch ran with install.prefetch=1" || true
grep -q "Gran Turismo 4" "$(echo "$WORK")/serial.txt" >/dev/null 2>&1 || true
ps2hdd list --ps2 --installed --no-artwork | grep -q "Gran Turismo 4" \
  || fail "the serial path did not install the title"
ps2hdd config set install.prefetch 2

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
# The per-game files go under __common/POPS, one directory per disc, and NOT
# beside the VCDs in __.POPS. Both partitions have a POPS-shaped directory and
# only one of them is read, so the partition is part of the assertion.
CD1="$DEMO/partitions/common/POPS/SLUS_005.94.Metal Gear Solid_CD1"
CD2="$DEMO/partitions/common/POPS/SLUS_007.76.Metal Gear Solid_CD2"
test -f "$CD1/DISCS.TXT" || fail "disc 1 has no DISCS.TXT"
test -f "$CD2/DISCS.TXT" || fail "disc 2 has no DISCS.TXT"
grep -q "_CD2.VCD" "$CD1/DISCS.TXT" || fail "DISCS.TXT does not list both discs"
# VMCDIR.TXT points the later discs at disc 1's card, or a save made on disc 1
# is gone after the swap. Disc 1 owns the card and must not have one.
test -f "$CD2/VMCDIR.TXT" || fail "disc 2 has no VMCDIR.TXT"
grep -q "_CD1.VCD" "$CD2/VMCDIR.TXT" || fail "VMCDIR.TXT does not name disc 1"
test -f "$CD1/VMCDIR.TXT" && fail "disc 1 was pointed at another card" || true
test -e "$DEMO/partitions/pops/SLUS_005.94.Metal Gear Solid" \
  && fail "support files were written into __.POPS, where POPStarter does not look" || true

step "--widescreen writes the directive, and only when asked"
test -f "$CD1/CHEATS.TXT" && fail "widescreen was applied without being asked for" || true
ps2hdd remove "Metal Gear Solid"
ps2hdd install --widescreen \
  "$DEMO/sources/psx/Metal Gear Solid/Disc 1.cue" \
  "$DEMO/sources/psx/Metal Gear Solid/Disc 2.cue" \
  --title "Metal Gear Solid"
grep -qx '\$WIDESCREEN' "$CD1/CHEATS.TXT" || fail "the widescreen directive was not written"
grep -qx '\$WIDESCREEN' "$CD2/CHEATS.TXT" || fail "disc 2 did not get the directive"
# A user's own codes in that file are theirs and must survive a reinstall.
printf '$SAFEMODE\n' > "$CD1/CHEATS.TXT"
ps2hdd remove "Metal Gear Solid"
ps2hdd install --widescreen \
  "$DEMO/sources/psx/Metal Gear Solid/Disc 1.cue" \
  "$DEMO/sources/psx/Metal Gear Solid/Disc 2.cue" \
  --title "Metal Gear Solid"
grep -qx '\$SAFEMODE' "$CD1/CHEATS.TXT" || fail "a user's own cheat code was discarded"
grep -qx '\$WIDESCREEN' "$CD1/CHEATS.TXT" || fail "the directive was not appended"

step "the predicted install size is the VCD that gets written"
# The estimate is what a space check acts on, so it is checked against the
# file, not against itself. A split rip is the case that used to be wrong:
# only the first FILE was measured, and the audio tracks went uncounted.
predicted=$(ps2hdd install --json --dry-run \
  "$DEMO/sources/psx/Final Fantasy VII/Disc 1.cue" \
  | python3 -c 'import json,sys; r=json.load(sys.stdin); print((r[0] if isinstance(r,list) else r)["game"]["install_size_bytes"])')
ps2hdd install "$DEMO/sources/psx/Final Fantasy VII/Disc 1.cue" --title "FF7 Size Check"
actual=$(stat -c%s "$DEMO"/partitions/pops/*FF7\ Size\ Check.VCD)
[ "$predicted" = "$actual" ] \
  || fail "predicted $predicted bytes, wrote $actual"
ps2hdd remove "FF7 Size Check"

step "the PS1 title got a launcher OPL can list"
# OPL has no PS1 support of its own: without this directory the game is on the
# disk, verified, and in no menu. The launcher points at disc 1; POPStarter
# swaps to disc 2 itself through DISCS.TXT.
LAUNCHER="$DEMO/partitions/plus-opl/APPS/SLUS_005.94.Metal Gear Solid_CD1"
test -f "$LAUNCHER/SLUS_005.94.Metal Gear Solid_CD1.ELF" \
  || fail "no POPStarter launcher was written"
test -f "$LAUNCHER/title.cfg" || fail "no title.cfg was written"
# OPL drops an entry that lacks either key, silently.
grep -q "^title=" "$LAUNCHER/title.cfg" || fail "title.cfg has no title key"
grep -q "^boot=SLUS_005.94.Metal Gear Solid_CD1.ELF$" "$LAUNCHER/title.cfg" \
  || fail "title.cfg does not boot the launcher beside it"
# The ELF is the POPStarter binary itself, not a stub.
cmp -s "$LAUNCHER/SLUS_005.94.Metal Gear Solid_CD1.ELF" \
  "$DEMO/partitions/common/POPS/POPSTARTER.ELF" \
  || fail "the launcher is not a copy of POPSTARTER.ELF"

step "doctor reports a title whose launcher is missing"
rm -rf "$LAUNCHER"
ps2hdd doctor > "$WORK/doctor-launchers.txt" 2>&1 || true
grep -q "no POPStarter launcher" "$WORK/doctor-launchers.txt" \
  || fail "doctor did not notice the missing launcher"

step "setup ps1 --launchers writes it back"
ps2hdd setup ps1 --launchers | tee "$WORK/launchers.txt"
grep -q "Metal Gear Solid" "$WORK/launchers.txt" || fail "the launcher was not repaired"
test -f "$LAUNCHER/title.cfg" || fail "title.cfg was not restored"
ps2hdd doctor > "$WORK/doctor-after.txt" 2>&1 || true
grep -q "no POPStarter launcher" "$WORK/doctor-after.txt" \
  && fail "doctor still reports a launcher it just wrote" || true

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
# The launcher lives on a different partition from the VCD, so removing only
# __.POPS would leave an Apps entry that boots nothing.
test -e "$LAUNCHER" && fail "the launcher survived removal" || true

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
