#!/usr/bin/env bash
#
# Cross-check ps2hdd's native APA/HDLoader reader against hdl_dump.
#
# ps2hdd parses the APA partition table and the HDLoader game headers itself
# rather than shelling out to hdl_dump (see docs/compatibility.md). That read
# path is the foundation everything else stands on: if it disagrees with the
# reference implementation, nothing downstream can be trusted, and no write
# should follow. This script is how you check.
#
# It is read-only. Neither tool is asked to modify anything.
#
#   scripts/crosscheck-hdl.sh /dev/disk/by-id/ata-YOUR_DRIVE
#   scripts/crosscheck-hdl.sh /dev/loop1            # a disk image, see below
#
# Raw block devices are root-owned, so this normally needs sudo.
#
# To check against a disk image rather than hardware, map it to a loop device
# first. udisksctl usually works without sudo via polkit:
#
#   udisksctl loop-setup -r -f ps2.img       # -r: read-only
#   sudo scripts/crosscheck-hdl.sh /dev/loop1
#   udisksctl loop-delete -b /dev/loop1
#
# hdl_dump cannot read an image file directly -- it needs a block device --
# which is why the loop device is necessary.

set -uo pipefail

DEV="${1:-}"
PS2HDD="${PS2HDD:-./ps2hdd}"
HDL="${HDL:-hdl_dump}"

if [ -z "$DEV" ]; then
  sed -n '3,28p' "$0" | sed 's/^# \{0,1\}//'
  exit 2
fi
command -v "$HDL" >/dev/null || { echo "hdl_dump not found; set HDL=/path/to/hdl_dump" >&2; exit 2; }
[ -x "$PS2HDD" ] || { echo "ps2hdd not found at $PS2HDD; set PS2HDD=/path/to/ps2hdd" >&2; exit 2; }
[ -r "$DEV" ] || { echo "cannot read $DEV -- run under sudo, or join the 'disk' group" >&2; exit 2; }

echo "device:   $DEV"
echo "ps2hdd:   $PS2HDD"
echo "hdl_dump: $HDL"
echo

ref=$("$HDL" hdl_toc "$DEV" --csv 2>/dev/null)
if [ -z "$ref" ]; then
  echo "hdl_dump produced no output -- is $DEV really a PS2 HDD?" >&2
  exit 1
fi

mine=$("$PS2HDD" --no-color --json --device "$DEV" list --ps2 --installed --no-artwork 2>/dev/null)
if [ -z "$mine" ]; then
  echo "ps2hdd produced no output for $DEV" >&2
  exit 1
fi

REF="$ref" MINE="$mine" python3 - <<'PY'
import json, os, re, sys

# hdl_dump hdl_toc --csv: "type;<n>KB;flags;dma;startup;name". The header row
# is printed space-separated even in CSV mode, so it is skipped by content.
ref = {}
for line in os.environ["REF"].splitlines():
    f = line.rstrip("\r").split(";")
    if len(f) < 6 or f[0].strip() not in ("CD", "DVD"):
        continue
    ref[re.sub(r"[^A-Z0-9]", "", f[4].strip().upper())] = {
        "media": f[0].strip(),
        "kb":    int(f[1].strip().removesuffix("KB")),
        "name":  ";".join(f[5:]).strip(),
    }

mine = {}
for e in json.loads(os.environ["MINE"]).get("entries") or []:
    mine[re.sub(r"[^A-Z0-9]", "", e["game_id"].upper())] = {
        "media": (e.get("media") or "").upper(),
        # ps2hdd reports the allocated footprint; hdl_toc reports the image
        # size. They are different numbers by design, so only the identity,
        # the name and the media type are compared.
        "name":  e["title"],
    }

fail = 0
print(f"hdl_dump sees {len(ref)} game(s); ps2hdd sees {len(mine)}")
if set(ref) != set(mine):
    fail = 1
    for k in sorted(set(ref) - set(mine)):
        print(f"  MISSING from ps2hdd: {k} ({ref[k]['name']})")
    for k in sorted(set(mine) - set(ref)):
        print(f"  EXTRA in ps2hdd:     {k} ({mine[k]['name']})")

print()
for k in sorted(set(ref) & set(mine)):
    r, m = ref[k], mine[k]
    notes = []
    if r["media"] != m["media"]:
        notes.append(f"media {r['media']} vs {m['media']}")
    if r["name"] != m["name"]:
        notes.append(f"name {r['name']!r} vs {m['name']!r}")
    if notes:
        fail = 1
        print(f"  DISAGREE {k}: " + "; ".join(notes))
    else:
        print(f"  agree    {k}  {r['media']:<3}  {r['name']}")

print()
if fail:
    print("MISMATCH -- the native reader disagrees with hdl_dump. Do not write to this disk.")
    sys.exit(1)
print("All entries agree.")
PY
