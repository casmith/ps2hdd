package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/asset/provider"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// AssetStatus reports artwork completeness for the installed library.
// WantedAndUnavailable splits the configured art slots into those the current
// provider can supply and those it cannot.
//
// Reporting on a slot no provider can fill is worse than not reporting it: it
// is a column of "no" that never becomes "yes", and it drags the completeness
// count to zero however healthy the library is. Those slots are named once,
// as a setup note, rather than per game.
//
// If the provider cannot be constructed at all, everything is reported as
// wanted; that failure is surfaced elsewhere and must not silently shrink the
// report here.
func (s *Services) WantedAndUnavailable() (want, unavailable []model.AssetType) {
	all := s.Config.WantedAssets()
	p, err := s.AssetProvider()
	if err != nil {
		return all, nil
	}
	unsupported := map[model.AssetType]bool{}
	for _, t := range provider.Unsupported(p, all) {
		unsupported[t] = true
	}
	for _, t := range all {
		if unsupported[t] {
			unavailable = append(unavailable, t)
			continue
		}
		want = append(want, t)
	}
	return want, unavailable
}

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
		want, _ := s.WantedAndUnavailable()
		rows = asset.Status(games, inv, want)
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
		// One clear failure beats one per file. A mount that will not take a
		// probe byte will not take any of the artwork either, and finding
		// that out now means the report names the cause instead of repeating
		// the same errno against thirty different paths.
		if err := asset.CheckWritable(mp); err != nil {
			return err
		}
		// Ensure the OPL directories exist before writing into them; a fresh
		// +OPL partition has neither ART nor CFG.
		for _, d := range []string{asset.ArtDir, asset.CfgDir} {
			if err := os.MkdirAll(filepath.Join(mp, d), 0o755); err != nil {
				return err
			}
		}
		res, err = mgr.Apply(ctx, plan, opts.OnProgress)
		if err != nil {
			return err
		}
		// A PS1 title reaches the console as an Apps entry, and OPL looks up an
		// app's artwork by its boot filename rather than by a serial. This puts
		// the same images under that name, including for titles whose artwork
		// was installed before that was understood.
		n, aerr := mgr.EnsureAppArtwork(games, mp)
		if aerr != nil {
			return aerr
		}
		if n > 0 {
			logging.ContextLogger(ctx).Info("wrote Apps-page artwork copies", "files", n)
		}
		return nil
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
// CreatePartitionReport is the outcome of creating an APA partition.
type CreatePartitionReport struct {
	Partition string `json:"partition"`
	Size      string `json:"size"`
	// Created is true only when the partition was found in the table after the
	// write, never merely because the tool was run.
	Created bool `json:"created"`
	// Script is the pfsshell input that was, or would be, fed in.
	Script string `json:"script,omitempty"`
	// Output is everything pfsshell printed, kept so a failure can be
	// explained in its own words.
	Output string `json:"output,omitempty"`
	DryRun bool   `json:"dry_run,omitempty"`
}

// CreatePOPSPartition creates the __.POPS partition through pfsshell.
//
// ps2hdd does not allocate APA space itself -- see the note at the top of
// internal/apa -- so this delegates to pfsshell exactly as installing a game
// delegates to hdl_dump. Reproducing APA's main-plus-sub-extent allocation
// would be reimplementing the one thing this project deliberately does not
// reimplement.
//
// What ps2hdd does add is a check. pfsshell is an interactive shell driven
// through stdin, so a failed `mkpart` prints "(!) Exit code is -1." and the
// shell then exits 0 regardless. The exit status carries no information, and
// the only trustworthy confirmation is reading the partition table back with
// the native reader.
func (s *Services) CreatePOPSPartition(ctx context.Context, size string) (CreatePartitionReport, error) {
	rep := CreatePartitionReport{Partition: ps1.POPSPartition, DryRun: s.DryRun}

	normalised, err := NormalisePartitionSize(size)
	if err != nil {
		return rep, err
	}
	rep.Size = normalised

	// The device is revalidated here, immediately before the write, rather
	// than relying on anything an earlier command established.
	t, err := s.Target(ctx, true)
	if err != nil {
		return rep, err
	}

	switch has, err := s.hasPartition(t, ps1.POPSPartition); {
	case err != nil:
		return rep, err
	case has:
		return rep, fmt.Errorf("%s already exists on %s", ps1.POPSPartition, t.Configured)
	}

	rep.Script = external.MkPartScript(t.Path, ps1.POPSPartition, normalised, "PFS")
	if s.DryRun {
		return rep, nil
	}

	s.hddLock.Lock()
	defer s.hddLock.Unlock()

	out, runErr := s.PFS.CreatePartition(ctx, t.Path, ps1.POPSPartition, normalised, "PFS")
	rep.Output = out
	if runErr != nil {
		return rep, fmt.Errorf("run pfsshell: %w", runErr)
	}

	has, err := s.hasPartition(t, ps1.POPSPartition)
	if err != nil {
		return rep, fmt.Errorf("confirm %s was created: %w", ps1.POPSPartition, err)
	}
	if !has {
		return rep, fmt.Errorf("pfsshell did not create %s. It reported:\n%s",
			ps1.POPSPartition, strings.TrimSpace(out))
	}
	rep.Created = true
	return rep, nil
}

// hasPartition reports whether the table currently holds a main partition with
// this id. It re-reads the device every time: it is used to check the state
// both before and after a write.
func (s *Services) hasPartition(t *drive.Target, id string) (bool, error) {
	f, err := os.Open(t.Path)
	if err != nil {
		return false, fmt.Errorf("open %s: %w", t.Path, err)
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, t.SizeBytes)
	if err != nil {
		return false, err
	}
	_, _, ok := toc.Find(id)
	return ok, nil
}

// NormalisePartitionSize validates a partition size and returns it in the form
// pfsshell expects.
//
// pfsshell wants a whole number followed by M or G. APA allocates in 128 MiB
// chunks, so a size that is not a whole number of chunks cannot be honoured as
// written; rejecting it is better than silently handing back something other
// than what was asked for. Every whole number of gigabytes qualifies.
func NormalisePartitionSize(size string) (string, error) {
	v := strings.ToUpper(strings.TrimSpace(size))
	if v == "" {
		return "", fmt.Errorf("a partition size is required, for example 20G")
	}
	unit := v[len(v)-1]
	if unit != 'M' && unit != 'G' {
		return "", fmt.Errorf("partition size %q must end in M or G, for example 20G", size)
	}
	n, err := strconv.Atoi(v[:len(v)-1])
	if err != nil || n <= 0 {
		return "", fmt.Errorf("partition size %q is not a positive whole number, for example 20G", size)
	}
	mb := n
	if unit == 'G' {
		mb = n * 1024
	}
	if mb < apa.ChunkMB {
		return "", fmt.Errorf("partition size %q is smaller than APA's %d MiB allocation unit", size, apa.ChunkMB)
	}
	if mb%apa.ChunkMB != 0 {
		return "", fmt.Errorf("partition size %q is not a multiple of APA's %d MiB allocation unit", size, apa.ChunkMB)
	}
	return v, nil
}

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
