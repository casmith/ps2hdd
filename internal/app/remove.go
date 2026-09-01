package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// RemoveOptions tune a removal.
type RemoveOptions struct {
	// PurgeAssets also deletes the game's artwork and configuration. It
	// defaults to false: artwork is small, is often hand-curated, and is
	// exactly what a user wants back if they reinstall.
	PurgeAssets bool
	OnProgress  ProgressFunc
}

// RemoveReport describes what a removal did, or would do under --dry-run.
type RemoveReport struct {
	Game model.Game `json:"game"`
	// Commands lists the external command lines involved.
	Commands [][]string `json:"commands,omitempty"`
	// Files lists paths deleted from the HDD.
	Files []string `json:"files,omitempty"`
	// Assets lists artwork removed, when PurgeAssets was set.
	Assets []string `json:"assets,omitempty"`
	// Script is the pfsshell input that was, or would be, fed in.
	Script string `json:"script,omitempty"`
	// Output is everything pfsshell printed, kept so a failure can be read.
	Output string `json:"output,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// FindInstalled resolves a user-supplied name or serial to exactly one
// installed title.
//
// Ambiguity is an error, never a choice made on the user's behalf: a wrong
// guess here deletes a game.
func (s *Services) FindInstalled(ctx context.Context, query string) (model.Game, error) {
	games, err := s.Installed(ctx)
	if err != nil {
		return model.Game{}, err
	}
	norm := model.NormalizeGameID(query)
	lower := strings.ToLower(strings.TrimSpace(query))

	if norm != "" {
		for _, g := range games {
			if model.NormalizeGameID(g.GameID) == norm {
				return g, nil
			}
		}
	}
	var exact, partial []model.Game
	for _, g := range games {
		t := strings.ToLower(g.Title)
		switch {
		case t == lower:
			exact = append(exact, g)
		case lower != "" && strings.Contains(t, lower):
			partial = append(partial, g)
		}
		// The APA partition name is a legitimate way to name a PS2 game.
		if g.PartitionName != "" && strings.EqualFold(g.PartitionName, query) {
			return g, nil
		}
	}
	matches := exact
	if len(matches) == 0 {
		matches = partial
	}
	switch len(matches) {
	case 0:
		return model.Game{}, &NotFoundError{Query: query}
	case 1:
		return matches[0], nil
	default:
		return model.Game{}, &AmbiguousError{Query: query, Matches: matches}
	}
}

// Remove deletes an installed title.
func (s *Services) Remove(ctx context.Context, g model.Game, opts RemoveOptions) (RemoveReport, error) {
	switch g.Platform {
	case model.PlatformPS2:
		return s.removePS2(ctx, g, opts)
	case model.PlatformPS1:
		return s.removePS1(ctx, g, opts)
	default:
		return RemoveReport{}, fmt.Errorf("unsupported platform %q", g.Platform)
	}
}

func (s *Services) removePS2(ctx context.Context, g model.Game, opts RemoveOptions) (RemoveReport, error) {
	rep := RemoveReport{Game: g, DryRun: s.DryRun}
	if !g.Installed {
		return rep, fmt.Errorf("%s is not installed", g.Title)
	}
	if g.PartitionName == "" {
		return rep, fmt.Errorf("%s has no recorded partition name; refusing to guess which partition to delete", g.Title)
	}

	opts.OnProgress.report(StageValidating, -1, "checking the HDD")
	t, err := s.Target(ctx, true)
	if err != nil {
		return rep, err
	}
	rep.Script = external.RmPartScript(t.Path, g.PartitionName)
	rep.Commands = append(rep.Commands, []string{external.PFSShellTool})
	if opts.PurgeAssets {
		names, err := s.assetPaths(ctx, g)
		if err == nil {
			rep.Assets = names
		}
	}
	if s.DryRun {
		return rep, nil
	}

	unlock := s.LockHDD()
	defer unlock()

	opts.OnProgress.report(StageRemoving, -1, g.Title)
	out, runErr := s.PFS.RemovePartition(ctx, t.Path, g.PartitionName)
	rep.Output = out
	if runErr != nil {
		return rep, fmt.Errorf("run pfsshell: %w", runErr)
	}
	// pfsshell exits 0 whether or not the command inside it worked, so the
	// only trustworthy confirmation is reading the table back.
	switch still, err := s.hasPartition(t, g.PartitionName); {
	case err != nil:
		return rep, fmt.Errorf("confirm %s was removed: %w", g.PartitionName, err)
	case still:
		return rep, fmt.Errorf("pfsshell did not remove %s. It reported:\n%s",
			g.PartitionName, strings.TrimSpace(out))
	}
	logging.ContextLogger(ctx).Info("removed PS2 game",
		"title", g.Title, "id", g.GameID, "partition", g.PartitionName)

	if opts.PurgeAssets {
		if err := s.purgeAssets(ctx, g); err != nil {
			logging.ContextLogger(ctx).Warn("artwork purge failed", "title", g.Title, "err", err)
		}
	}
	opts.OnProgress.report(StageComplete, 1, g.Title)
	return rep, nil
}

func (s *Services) removePS1(ctx context.Context, g model.Game, opts RemoveOptions) (RemoveReport, error) {
	rep := RemoveReport{Game: g, DryRun: s.DryRun}
	if !g.Installed {
		return rep, fmt.Errorf("%s is not installed", g.Title)
	}
	opts.OnProgress.report(StageValidating, -1, "checking the HDD")
	if _, err := s.Target(ctx, true); err != nil {
		return rep, err
	}

	// Removing a multi-disc title removes every disc: the user thinks of it as
	// one game, and leaving disc 2 behind would be a surprise.
	var names []string
	for _, d := range g.Discs {
		if d.InstalledName != "" {
			names = append(names, d.InstalledName)
		}
	}
	if len(names) == 0 {
		return rep, fmt.Errorf("%s has no recorded VCD files; refusing to guess what to delete", g.Title)
	}
	for _, n := range names {
		rep.Files = append(rep.Files, ps1.POPSPartition+"/"+n)
	}
	// The per-game support directories are in __common, one per disc.
	for _, n := range names {
		rep.Files = append(rep.Files,
			ps1.CommonPartition+"/"+ps1.POPSDir+"/"+ps1.GameDirName(n)+"/")
	}
	// The launcher lives on a different partition from the VCD, so removing
	// only __.POPS would leave an entry on OPL's Apps page that boots nothing.
	launcherDir := ps1.LauncherDirName(names[0])
	launcherELF := ps1.LauncherELFName(names[0])
	rep.Files = append(rep.Files,
		drive.PartitionOPL+"/"+ps1.AppsDir+"/"+launcherDir+"/"+launcherELF,
		drive.PartitionOPL+"/"+ps1.AppsDir+"/"+launcherDir+"/"+ps1.TitleConfigFile)
	if opts.PurgeAssets {
		if paths, err := s.assetPaths(ctx, g); err == nil {
			rep.Assets = paths
		}
	}
	if s.DryRun {
		return rep, nil
	}

	unlock := s.LockHDD()
	defer unlock()

	m, err := s.Mounts(ctx)
	if err != nil {
		return rep, err
	}
	opts.OnProgress.report(StageRemoving, -1, g.Title)
	err = m.With(ctx, ps1.POPSPartition, func(mp string) error {
		for _, n := range names {
			if err := os.Remove(filepath.Join(mp, n)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", n, err)
			}
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	// The support files ps2hdd wrote go with the game. Anything else in the
	// directory is the user's -- their cheat codes, a virtual memory card with
	// their saves in it -- so the directory only goes when it is empty.
	if err := m.With(ctx, ps1.CommonPartition, func(mp string) error {
		for _, n := range names {
			dir := filepath.Join(mp, ps1.POPSDir, ps1.GameDirName(n))
			for _, f := range []string{ps1.DiscsFile, ps1.VMCDirFile} {
				if err := os.Remove(filepath.Join(dir, f)); err != nil && !os.IsNotExist(err) {
					return fmt.Errorf("remove %s: %w", f, err)
				}
			}
			// A CHEATS.TXT holding nothing but the directive ps2hdd put there
			// is ps2hdd's to clean up. One the user has added to is theirs,
			// and it stays -- along with the directory around it.
			removeIfOnlyOurs(filepath.Join(dir, ps1.CheatsFile), ps1.Widescreen)
			if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
				_ = os.Remove(dir)
			}
		}
		return nil
	}); err != nil {
		logging.ContextLogger(ctx).Warn("could not remove the POPStarter support files",
			"title", g.Title, "err", err)
	}
	// A launcher that cannot be removed is a dead menu entry, not a lost game,
	// so it is reported rather than allowed to fail a removal that has already
	// deleted the discs.
	if err := m.With(ctx, drive.PartitionOPL, func(mp string) error {
		dir := filepath.Join(mp, ps1.AppsDir, launcherDir)
		for _, n := range []string{launcherELF, ps1.TitleConfigFile} {
			if err := os.Remove(filepath.Join(dir, n)); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("remove %s: %w", n, err)
			}
		}
		// Only if it is now empty: a user may keep their own files there.
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			_ = os.Remove(dir)
		}
		return nil
	}); err != nil {
		logging.ContextLogger(ctx).Warn("could not remove the POPStarter launcher", "title", g.Title, "err", err)
	}
	logging.ContextLogger(ctx).Info("removed PS1 game", "title", g.Title, "discs", len(names))

	if opts.PurgeAssets {
		if err := s.purgeAssets(ctx, g); err != nil {
			logging.ContextLogger(ctx).Warn("artwork purge failed", "title", g.Title, "err", err)
		}
	}
	opts.OnProgress.report(StageComplete, 1, g.Title)
	return rep, nil
}

// assetPaths lists the artwork and configuration files belonging to a game
// that actually exist on the HDD.
// removeIfOnlyOurs deletes a file whose entire content is the given directive.
func removeIfOnlyOurs(path, directive string) {
	body, err := os.ReadFile(path)
	if err != nil {
		return
	}
	if strings.TrimSpace(string(body)) == directive {
		_ = os.Remove(path)
	}
}

func (s *Services) assetPaths(ctx context.Context, g model.Game) ([]string, error) {
	m, err := s.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	var out []string
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		types := append(append([]model.AssetType{}, model.ArtTypes...), model.AssetConfig)
		for _, t := range types {
			p := asset.Path(mp, g.GameID, t)
			if _, err := os.Stat(p); err == nil {
				out = append(out, asset.Dir(t)+"/"+asset.Filename(g.GameID, t))
			}
		}
		return nil
	})
	return out, err
}

// purgeAssets deletes a game's artwork and configuration.
func (s *Services) purgeAssets(ctx context.Context, g model.Game) error {
	m, err := s.Mounts(ctx)
	if err != nil {
		return err
	}
	return m.With(ctx, drive.PartitionOPL, func(mp string) error {
		types := append(append([]model.AssetType{}, model.ArtTypes...), model.AssetConfig)
		for _, t := range types {
			p := asset.Path(mp, g.GameID, t)
			if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
				return err
			}
		}
		return nil
	})
}
