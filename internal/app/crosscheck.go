package app

import (
	"context"
	"fmt"
	"os"
	"sort"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/model"
)

// CrossCheck compares ps2hdd's native APA reader against hdl_dump.
//
// ps2hdd parses the APA table and the HDLoader headers itself rather than
// shelling out, which makes the read path fast, image-friendly and testable --
// and makes it ps2hdd's own problem if it is wrong. Every listing, every size
// figure and every decision about what is already installed rests on that
// parse. Comparing it against the reference implementation is the one check
// that can tell you the foundation is sound.
//
// Sizes are deliberately not compared. hdl_dump reports the size of the game
// image; ps2hdd reports the APA footprint it occupies, rounded up to whole
// 128 MiB chunks. They are different numbers by design and always will be, so
// only identity, title and media type are checked.
type CrossCheck struct {
	// Ran is true only when both readers produced a list that was compared.
	// It is false whenever the comparison could not be made, and callers must
	// not read that as agreement.
	Ran bool `json:"ran"`
	// Unavailable says why the comparison could not run: hdl_dump absent, or
	// unable to read the device. It is not a fault in the library.
	Unavailable string `json:"unavailable,omitempty"`

	NativeGames    int `json:"native_games"`
	ReferenceGames int `json:"reference_games"`

	// Disagreements lists every difference found, in game-id order.
	Disagreements []string `json:"disagreements,omitempty"`
}

// Agree reports whether the two readers described the same library. It is
// false when the check did not run, because "not checked" is not "agreed".
func (c CrossCheck) Agree() bool { return c.Ran && len(c.Disagreements) == 0 }

// CrossCheckReader reads the installed PS2 library twice -- once natively,
// once through hdl_dump -- and reports where the two disagree.
//
// A missing hdl_dump is not a failure: reading needs no external tools, so the
// check simply cannot run and says so. The same goes for a device hdl_dump
// cannot open, which normally means it was run without root.
func (s *Services) CrossCheckReader(ctx context.Context) (CrossCheck, error) {
	var cc CrossCheck

	t, err := s.Target(ctx, false)
	if err != nil {
		return cc, err
	}
	if _, ok := s.HDL.Available(); !ok {
		cc.Unavailable = fmt.Sprintf("%s is not installed", external.HDLDumpTool)
		return cc, nil
	}

	native, err := nativeGames(t)
	if err != nil {
		return cc, err
	}
	ref, err := s.HDL.ListGames(ctx, t.Path)
	if err != nil {
		// hdl_toc needs raw block access, so this is usually "not root". The
		// native read already succeeded, so the library is fine; it is the
		// comparison that is unavailable.
		cc.Unavailable = fmt.Sprintf("%s could not read %s: %v", external.HDLDumpTool, t.Path, err)
		return cc, nil
	}

	type entry struct {
		name  string
		isDVD bool
	}
	mine := map[string]entry{}
	for _, g := range native {
		mine[model.NormalizeGameID(g.Startup)] = entry{name: g.Name, isDVD: g.IsDVD}
	}
	theirs := map[string]entry{}
	for _, g := range ref.Games {
		theirs[model.NormalizeGameID(g.Startup)] = entry{name: g.Name, isDVD: g.IsDVD}
	}

	cc.Ran = true
	cc.NativeGames = len(mine)
	cc.ReferenceGames = len(theirs)

	seen := map[string]bool{}
	var ids []string
	for id := range theirs {
		ids = append(ids, id)
		seen[id] = true
	}
	for id := range mine {
		if !seen[id] {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)

	for _, id := range ids {
		r, inRef := theirs[id]
		n, inMine := mine[id]
		switch {
		case !inMine:
			cc.Disagreements = append(cc.Disagreements,
				fmt.Sprintf("%s (%s) is in hdl_dump's list but missing from ps2hdd's", id, r.name))
		case !inRef:
			cc.Disagreements = append(cc.Disagreements,
				fmt.Sprintf("%s (%s) is in ps2hdd's list but missing from hdl_dump's", id, n.name))
		default:
			if n.name != r.name {
				cc.Disagreements = append(cc.Disagreements,
					fmt.Sprintf("%s: ps2hdd reads the title as %q, hdl_dump as %q", id, n.name, r.name))
			}
			if n.isDVD != r.isDVD {
				cc.Disagreements = append(cc.Disagreements,
					fmt.Sprintf("%s: ps2hdd reads the media as %s, hdl_dump as %s",
						id, mediaLabel(n.isDVD), mediaLabel(r.isDVD)))
			}
		}
	}
	return cc, nil
}

func mediaLabel(isDVD bool) string {
	if isDVD {
		return "DVD"
	}
	return "CD"
}

// nativeGames reads the HDLoader titles straight from the APA table.
func nativeGames(t *drive.Target) ([]apa.GameInfo, error) {
	f, err := os.Open(t.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", t.Path, err)
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, t.SizeBytes)
	if err != nil {
		return nil, err
	}
	return apa.ReadGames(f, toc)
}
