package catalog

import (
	"context"
	"fmt"
	"os"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// InstalledReader reads what is actually on a PS2 HDD.
//
// The HDD is the only authority on installed state: nothing here consults the
// source directories or any cache. PS2 titles come from the APA table's
// HDLoader partitions, read natively; PS1 titles come from the VCD files in
// the __.POPS partition, which needs a PFS mount.
type InstalledReader struct {
	Target *drive.Target
	Mounts *drive.MountManager
}

// PartialError reports that some of the installed library was read and some
// was not.
//
// The distinction matters more than it looks. A caller that cannot tell a
// partial read from a complete one will treat the missing half as "not
// installed", and the install path acts on exactly that judgement. Anything
// that fails without this wrapper means nothing could be read at all.
type PartialError struct{ Err error }

func (e *PartialError) Error() string { return e.Err.Error() }

func (e *PartialError) Unwrap() error { return e.Err }

// PS2Games lists the installed HDLoader titles.
func (r InstalledReader) PS2Games(ctx context.Context) ([]model.Game, error) {
	f, err := os.Open(r.Target.Path)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", r.Target.Path, err)
	}
	defer f.Close()

	toc, err := apa.ReadTOC(f, r.Target.SizeBytes)
	if err != nil {
		return nil, err
	}
	infos, err := apa.ReadGames(f, toc)
	if err != nil {
		return nil, err
	}
	return PS2GamesFrom(infos), nil
}

// PS2GamesFrom turns hdl_dump's game descriptors into catalog entries.
//
// It is separate from the read so that a caller who already has the partition
// table in hand -- the install path reads it once for both of its pre-write
// checks -- gets the same entries rather than a second, subtly different
// mapping of its own.
func PS2GamesFrom(infos []apa.GameInfo) []model.Game {
	games := make([]model.Game, 0, len(infos))
	for _, g := range infos {
		media := model.MediaCD
		if g.IsDVD {
			media = model.MediaDVD
		}
		id := model.OPLGameID(g.Startup)
		title := g.Name
		if title == "" {
			title = id
		}
		games = append(games, model.Game{
			Platform:         model.PlatformPS2,
			Title:            title,
			GameID:           id,
			SizeBytes:        g.SizeBytes(),
			InstallSizeBytes: g.SizeBytes(),
			StorageBackend:   model.BackendHDL,
			Media:            media,
			Installed:        true,
			PartitionName:    g.PartitionName,
			Discs: []model.Disc{{
				Number:           1,
				GameID:           id,
				Title:            title,
				InstalledName:    g.PartitionName,
				SizeBytes:        g.SizeBytes(),
				InstallSizeBytes: g.SizeBytes(),
			}},
		})
	}
	model.SortGames(games)
	return games
}

// PS1Games lists the installed POPStarter titles.
//
// A drive with no __.POPS partition has no PS1 games, which is a normal state
// and not an error: plenty of setups are PS2-only.
func (r InstalledReader) PS1Games(ctx context.Context) ([]model.Game, error) {
	has, err := r.hasPartition(ps1.POPSPartition)
	if err != nil {
		return nil, err
	}
	if !has {
		return nil, nil
	}
	if r.Mounts == nil {
		return nil, fmt.Errorf("reading PS1 games needs a mount manager")
	}
	var games []model.Game
	err = r.Mounts.With(ctx, ps1.POPSPartition, func(mp string) error {
		g, err := ps1.ScanPOPS(mp)
		if err != nil {
			return err
		}
		games = g
		return nil
	})
	if err != nil {
		return nil, err
	}
	model.SortGames(games)
	return games, nil
}

// Readiness reports whether PS1 support is set up on this drive.
func (r InstalledReader) Readiness(ctx context.Context) (ps1.Readiness, error) {
	var out ps1.Readiness
	var err error
	if out.POPSPartition, err = r.hasPartition(ps1.POPSPartition); err != nil {
		return out, err
	}
	if out.CommonPartition, err = r.hasPartition(ps1.CommonPartition); err != nil {
		return out, err
	}
	if !out.CommonPartition || r.Mounts == nil {
		return out, nil
	}
	// A failure to mount __common leaves the runtime status unknown rather
	// than reported as missing; saying "missing" would send the user chasing a
	// file that may well be there.
	mountErr := r.Mounts.With(ctx, ps1.CommonPartition, func(mp string) error {
		present, missing, wrong, err := ps1.CheckRuntime(mp)
		if err != nil {
			return err
		}
		out.Runtime, out.Missing, out.Wrong, out.RuntimeChecked = present, missing, wrong, true
		return nil
	})
	if mountErr != nil {
		return out, nil
	}
	return out, nil
}

func (r InstalledReader) hasPartition(id string) (bool, error) {
	f, err := os.Open(r.Target.Path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", r.Target.Path, err)
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, r.Target.SizeBytes)
	if err != nil {
		return false, err
	}
	_, _, ok := toc.Find(id)
	return ok, nil
}

// All lists every installed title on both platforms.
func (r InstalledReader) All(ctx context.Context) ([]model.Game, error) {
	ps2Games, err := r.PS2Games(ctx)
	if err != nil {
		// The PS2 half is read natively from the APA table. If that fails
		// there is no library to report, and saying so is the whole point:
		// an empty list is indistinguishable from an empty disk, and callers
		// that write act on that difference.
		return nil, err
	}
	ps1Games, err := r.PS1Games(ctx)
	if err != nil {
		// PS1 enumeration needs a FUSE mount, which can fail for reasons that
		// have nothing to do with the PS2 half of the library. Reporting the
		// PS2 games plus the reason is more useful than reporting nothing --
		// but it is flagged as partial so a caller can tell the difference
		// between "this is the library" and "this is most of the library".
		return ps2Games, &PartialError{Err: fmt.Errorf("list PS1 games: %w", err)}
	}
	all := append(ps2Games, ps1Games...)
	model.SortGames(all)
	return all, nil
}
