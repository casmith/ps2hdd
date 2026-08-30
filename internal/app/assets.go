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

// AssetStatus reports artwork completeness for the installed library.
func (s *Services) AssetStatus(ctx context.Context, games []model.Game) ([]asset.StatusRow, error) {
	if games == nil {
		// A PS1 listing failure must not hide the PS2 games' artwork status:
		// Installed returns what it could read alongside the error.
		got, err := s.Installed(ctx)
		if err != nil && len(got) == 0 {
			return nil, err
		}
		games = got
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	var rows []asset.StatusRow
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		inv, err := asset.Scan(mp)
		if err != nil {
			return err
		}
		rows = asset.Status(games, inv, s.Config.WantedAssets())
		return nil
	})
	return rows, missingTool(err, external.PFSFuseTool, "Artwork status")
}

// SyncAssetsOptions tune an artwork sync.
type SyncAssetsOptions struct {
	// Overwrite replaces artwork already on the HDD.
	Overwrite bool
	// MissingOnly is the default; when false with Overwrite set, every wanted
	// slot is refetched.
	OnProgress func(done, total int, item asset.PlanItem)
}

// SyncAssets fetches and installs missing artwork for the given games.
func (s *Services) SyncAssets(ctx context.Context, games []model.Game, opts SyncAssetsOptions) (asset.Plan, asset.Result, error) {
	var plan asset.Plan
	var res asset.Result

	if games == nil {
		var err error
		if games, err = s.Installed(ctx); err != nil {
			return plan, res, err
		}
	}
	mgr, err := s.AssetManager(opts.Overwrite)
	if err != nil {
		return plan, res, err
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return plan, res, err
	}

	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		inv, err := asset.Scan(mp)
		if err != nil {
			return err
		}
		plan, err = mgr.PlanSync(ctx, games, inv, mp)
		if err != nil {
			return err
		}
		if s.DryRun {
			return nil
		}
		// Ensure the OPL directories exist before writing into them; a fresh
		// +OPL partition has neither ART nor CFG.
		for _, d := range []string{asset.ArtDir, asset.CfgDir} {
			if err := os.MkdirAll(filepath.Join(mp, d), 0o755); err != nil {
				return err
			}
		}
		res, err = mgr.Apply(ctx, plan, opts.OnProgress)
		return err
	})
	return plan, res, missingTool(err, external.PFSFuseTool, "Artwork sync")
}

// CleanAssetCache empties the artwork download cache.
func (s *Services) CleanAssetCache() (int, int64, error) {
	c, err := asset.OpenDownloadCache()
	if err != nil {
		return 0, 0, err
	}
	size, _ := c.Size()
	n, err := c.Clean()
	return n, size, err
}

// SetupPS1Options tune PS1 setup.
type SetupPS1Options struct {
	// ImportDir holds user-supplied runtime files to copy into __common/POPS.
	ImportDir string
}

// SetupPS1Report is the outcome of `ps2hdd setup ps1`.
type SetupPS1Report struct {
	Readiness ps1.Readiness `json:"readiness"`
	// Imported lists runtime files copied in.
	Imported []string `json:"imported,omitempty"`
	// Ignored lists files in the import directory that are not part of the
	// runtime, so the user learns why nothing happened to them.
	Ignored []string `json:"ignored,omitempty"`
	DryRun  bool     `json:"dry_run,omitempty"`
}

// SetupPS1 inspects PS1 support and optionally imports user-supplied runtime
// files.
//
// ps2hdd never ships POPS.ELF or IOPRP252.IMG: they are Sony code with no
// redistribution rights. What it does is detect their absence, explain it, and
// copy in files the user has obtained themselves.
func (s *Services) SetupPS1(ctx context.Context, opts SetupPS1Options) (SetupPS1Report, error) {
	rep := SetupPS1Report{DryRun: s.DryRun}
	ready, err := s.PS1Readiness(ctx)
	if err != nil {
		return rep, err
	}
	rep.Readiness = ready
	if opts.ImportDir == "" {
		return rep, nil
	}
	if !ready.CommonPartition {
		return rep, fmt.Errorf("%w: the %s partition does not exist, so there is nowhere to import the runtime",
			ErrNotReady, ps1.CommonPartition)
	}

	entries, err := os.ReadDir(opts.ImportDir)
	if err != nil {
		return rep, fmt.Errorf("read import directory: %w", err)
	}
	// Only files that are part of the documented runtime are copied. Copying
	// a whole directory onto the HDD would put unknown content in a partition
	// the console boots from.
	wanted := map[string]string{}
	for _, f := range ps1.RuntimeFiles {
		wanted[strings.ToUpper(f.Name)] = f.Name
	}
	var toCopy [][2]string // source path, destination name
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		canonical, ok := wanted[strings.ToUpper(e.Name())]
		if !ok {
			rep.Ignored = append(rep.Ignored, e.Name())
			continue
		}
		toCopy = append(toCopy, [2]string{filepath.Join(opts.ImportDir, e.Name()), canonical})
	}
	if len(toCopy) == 0 {
		return rep, fmt.Errorf("%s contains none of the POPS runtime files (%s)",
			opts.ImportDir, runtimeNames())
	}
	for _, c := range toCopy {
		rep.Imported = append(rep.Imported, ps1.CommonPartition+"/"+ps1.POPSDir+"/"+c[1])
	}
	if s.DryRun {
		return rep, nil
	}

	if _, err := s.Target(ctx, true); err != nil {
		return rep, err
	}
	unlock := s.LockHDD()
	defer unlock()

	m, err := s.Mounts(ctx)
	if err != nil {
		return rep, err
	}
	err = m.With(ctx, ps1.CommonPartition, func(mp string) error {
		dir := filepath.Join(mp, ps1.POPSDir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("create %s/%s: %w", ps1.CommonPartition, ps1.POPSDir, err)
		}
		for _, c := range toCopy {
			if err := copyFile(c[0], filepath.Join(dir, c[1])); err != nil {
				return fmt.Errorf("copy %s: %w", c[1], err)
			}
			logging.ContextLogger(ctx).Info("imported POPS runtime file", "name", c[1])
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	// Re-read so the report shows the state after the import, not before it.
	if r, err := s.PS1Readiness(ctx); err == nil {
		rep.Readiness = r
	}
	return rep, nil
}

func runtimeNames() string {
	names := make([]string, 0, len(ps1.RuntimeFiles))
	for _, f := range ps1.RuntimeFiles {
		names = append(names, f.Name)
	}
	return strings.Join(names, ", ")
}
