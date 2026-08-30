package app

import (
	"context"
	"fmt"
	"os"
	"sync"

	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/asset/provider"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// Services is the object the CLI and TUI both drive.
//
// It owns the pieces whose lifetime spans an operation: the resolved device,
// the mount manager whose mounts must be released on exit, and the caches. A
// Services must be closed.
type Services struct {
	Config config.Config
	Runner external.Runner

	HDL external.HDLDump
	PFS external.PFS

	Registry *provider.Registry

	// DryRun makes every mutating operation report what it would do without
	// touching the disk.
	DryRun bool
	// AssumeYes suppresses interactive confirmation. It never bypasses a
	// safety refusal.
	AssumeYes bool

	mu       sync.Mutex
	target   *drive.Target
	mounts   *drive.MountManager
	srcCache map[string]*catalog.Cache
	// hddLock serialises raw-HDD mutations. hdl_dump makes no promise about
	// concurrent writers to one disk, so ps2hdd allows exactly one at a time.
	hddLock sync.Mutex
}

// New builds the service layer from a configuration.
func New(cfg config.Config, runner external.Runner) *Services {
	s := &Services{
		Config:   cfg,
		Runner:   runner,
		Registry: provider.NewRegistry(),
		srcCache: map[string]*catalog.Cache{},
	}
	s.HDL = external.HDLDump{Runner: runner}
	s.PFS = external.PFS{Runner: runner}
	return s
}

// NewFromConfig builds services with a real command runner.
func NewFromConfig(cfg config.Config) *Services {
	return New(cfg, &external.ExecRunner{
		Sudo: cfg.Tools.Sudo,
		Overrides: map[string]string{
			external.HDLDumpTool:  cfg.Tools.HDLDump,
			external.PFSFuseTool:  cfg.Tools.PFSFuse,
			external.PFSShellTool: cfg.Tools.PFSShell,
		},
	})
}

// Close releases every mount this process created.
func (s *Services) Close(ctx context.Context) error {
	s.mu.Lock()
	m := s.mounts
	s.mounts = nil
	s.mu.Unlock()
	if m == nil {
		return nil
	}
	return m.Close(ctx)
}

// Target validates the configured device and caches the result for the rest of
// the session.
//
// Read and write validations are cached separately in effect: a write always
// revalidates, because a device can be unplugged between the read that listed
// the library and the write that modifies it.
func (s *Services) Target(ctx context.Context, write bool) (*drive.Target, error) {
	if write {
		// Never reuse a cached validation for a write. This is the "verify
		// device again immediately before write" invariant.
		return s.validate(ctx, true)
	}
	s.mu.Lock()
	t := s.target
	s.mu.Unlock()
	if t != nil {
		return t, nil
	}
	return s.validate(ctx, false)
}

func (s *Services) validate(ctx context.Context, write bool) (*drive.Target, error) {
	if s.Config.Device == "" {
		return nil, ErrNoDevice
	}
	t, err := drive.Validate(ctx, s.Config.Device, drive.Options{
		Runner:     s.Runner,
		Write:      write,
		RequireAPA: true,
	})
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	s.target = t
	if s.mounts == nil {
		s.mounts = drive.NewMountManager(s.PFS, t.Path)
	}
	s.mu.Unlock()
	return t, nil
}

// Mounts returns the mount manager, validating the device first.
func (s *Services) Mounts(ctx context.Context) (*drive.MountManager, error) {
	if _, err := s.Target(ctx, false); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mounts, nil
}

// Status returns the drive overview.
func (s *Services) Status(ctx context.Context) (model.DriveStatus, error) {
	t, err := s.Target(ctx, false)
	if err != nil {
		return model.DriveStatus{}, err
	}
	st, err := drive.Status(ctx, t)
	if err != nil {
		return st, err
	}
	// Counting PS1 games needs a PFS mount, which may be unavailable. The rest
	// of the status is still worth reporting, so a failure is a note.
	games, err := s.InstalledPS1(ctx)
	if err != nil {
		st.Notes = append(st.Notes, "PS1 games could not be counted: "+err.Error())
	} else {
		st.PS1Games = len(games)
	}
	return st, nil
}

// Detect enumerates candidate PS2 HDDs, read-only.
func (s *Services) Detect(ctx context.Context) ([]drive.Candidate, error) {
	return drive.Detect(ctx, s.Runner)
}

// reader builds an installed-library reader.
func (s *Services) reader(ctx context.Context) (catalog.InstalledReader, error) {
	t, err := s.Target(ctx, false)
	if err != nil {
		return catalog.InstalledReader{}, err
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return catalog.InstalledReader{}, err
	}
	return catalog.InstalledReader{Target: t, Mounts: m}, nil
}

// InstalledPS2 lists installed PS2 titles.
func (s *Services) InstalledPS2(ctx context.Context) ([]model.Game, error) {
	r, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	return r.PS2Games(ctx)
}

// InstalledPS1 lists installed PS1 titles.
//
// PS1 titles live in a PFS partition, so this is the one listing that needs an
// external tool. A missing one is reported as a setup gap rather than a raw
// "external tool not found" from deep in the mount code.
func (s *Services) InstalledPS1(ctx context.Context) ([]model.Game, error) {
	r, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	games, err := r.PS1Games(ctx)
	return games, missingTool(err, external.PFSFuseTool, "PlayStation 1 games")
}

// Installed lists every installed title.
//
// The PS2 half comes from the native APA reader and always succeeds; the PS1
// half needs a PFS mount. When only the PS1 half fails the PS2 games are still
// returned alongside the error, because a partial library is more useful than
// none.
func (s *Services) Installed(ctx context.Context) ([]model.Game, error) {
	r, err := s.reader(ctx)
	if err != nil {
		return nil, err
	}
	games, err := r.All(ctx)
	return games, missingTool(err, external.PFSFuseTool, "PlayStation 1 games")
}

// PS1Readiness reports whether POPStarter is set up.
func (s *Services) PS1Readiness(ctx context.Context) (ps1.Readiness, error) {
	r, err := s.reader(ctx)
	if err != nil {
		return ps1.Readiness{}, err
	}
	return r.Readiness(ctx)
}

// sourceCache returns the persistent scan cache for an index.
func (s *Services) sourceCache(name string) *catalog.Cache {
	s.mu.Lock()
	defer s.mu.Unlock()
	if c, ok := s.srcCache[name]; ok {
		return c
	}
	c, err := catalog.OpenCache(name)
	if err != nil {
		// A cache that cannot be opened degrades to no caching, never to a
		// failed scan.
		c = catalog.NewMemoryCache()
	}
	s.srcCache[name] = c
	return c
}

// ScanSources scans both configured source directories.
func (s *Services) ScanSources(ctx context.Context) (ps2Res, ps1Res catalog.ScanResult, err error) {
	if dir := s.Config.Sources.PS2; dir != "" {
		sc := catalog.NewScanner(s.sourceCache("ps2"))
		ps2Res, err = sc.ScanPS2(ctx, dir)
		if err != nil {
			return ps2Res, ps1Res, fmt.Errorf("scan PS2 sources: %w", err)
		}
	}
	if dir := s.Config.Sources.PS1; dir != "" {
		sc := catalog.NewScanner(s.sourceCache("ps1"))
		ps1Res, err = sc.ScanPS1(ctx, dir)
		if err != nil {
			return ps2Res, ps1Res, fmt.Errorf("scan PS1 sources: %w", err)
		}
	}
	return ps2Res, ps1Res, nil
}

// ClearSourceCache discards the cached scan results.
func (s *Services) ClearSourceCache() error {
	var firstErr error
	for _, name := range []string{"ps2", "ps1"} {
		if err := s.sourceCache(name).Clear(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// Catalog builds the reconciled library: installed titles from the HDD, source
// titles from the configured directories, and the artwork gap between them.
//
// A failure to read the HDD is not fatal: the source half is still useful, and
// showing it with an explanation beats showing nothing.
func (s *Services) Catalog(ctx context.Context) (catalog.Catalog, []error) {
	var warnings []error

	installed, err := s.Installed(ctx)
	if err != nil {
		warnings = append(warnings, missingTool(err, external.PFSFuseTool, "PlayStation 1 games"))
	}

	ps2Res, ps1Res, err := s.ScanSources(ctx)
	if err != nil {
		warnings = append(warnings, err)
	}
	source := append(append([]model.Game{}, ps2Res.Games...), ps1Res.Games...)
	problems := append(append([]catalog.ScanProblem{}, ps2Res.Problems...), ps1Res.Problems...)

	c := catalog.Reconcile(installed, source, problems)

	if err := s.annotateAssets(ctx, &c); err != nil {
		warnings = append(warnings, missingTool(err, external.PFSFuseTool, "Artwork status"))
	}
	return c, warnings
}

// annotateAssets fills in MissingAssets for the installed entries.
func (s *Services) annotateAssets(ctx context.Context, c *catalog.Catalog) error {
	want := s.Config.WantedAssets()
	if len(want) == 0 {
		return nil
	}
	hasInstalled := false
	for _, e := range c.Entries {
		if e.Installed {
			hasInstalled = true
			break
		}
	}
	if !hasInstalled {
		return nil
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return err
	}
	var inv *asset.Inventory
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		i, err := asset.Scan(mp)
		if err != nil {
			return err
		}
		inv = i
		return nil
	})
	if err != nil {
		return err
	}
	for i := range c.Entries {
		if !c.Entries[i].Installed {
			continue
		}
		c.Entries[i].MissingAssets = inv.Missing(c.Entries[i].GameID, want)
		c.Entries[i].AssetsKnown = true
	}
	return nil
}

// AssetProvider builds the configured artwork provider.
//
// When a mirror is configured alongside a remote provider the two are chained,
// mirror first: a local file is faster, always available, and is what a user
// who curated artwork by hand expects to win.
func (s *Services) AssetProvider() (provider.Provider, error) {
	opts := provider.Options{
		Mirror:    s.Config.Assets.Mirror,
		Templates: s.Config.Assets.Templates,
	}
	var chain []provider.Provider
	if opts.Mirror != "" && s.Config.Assets.Provider != "local" {
		if p, err := s.Registry.New("local", opts); err == nil {
			chain = append(chain, p)
		}
	}
	p, err := s.Registry.New(s.Config.Assets.Provider, opts)
	if err != nil {
		return nil, err
	}
	chain = append(chain, p)
	if len(chain) == 1 {
		return chain[0], nil
	}
	return provider.Chain{Providers: chain}, nil
}

// AssetManager builds an artwork manager.
func (s *Services) AssetManager(overwrite bool) (*asset.Manager, error) {
	p, err := s.AssetProvider()
	if err != nil {
		return nil, err
	}
	cache, err := asset.OpenDownloadCache()
	if err != nil {
		return nil, err
	}
	return &asset.Manager{
		Provider:  p,
		Cache:     cache,
		Want:      s.Config.WantedAssets(),
		Overwrite: overwrite,
	}, nil
}

// LockHDD serialises raw-HDD mutations. Callers must call the returned
// function when done.
func (s *Services) LockHDD() func() {
	s.hddLock.Lock()
	return s.hddLock.Unlock
}

// FreeSpace reports the unallocated APA space in bytes.
func (s *Services) FreeSpace(ctx context.Context) (int64, error) {
	st, err := s.Status(ctx)
	if err != nil {
		return 0, err
	}
	return st.FreeBytes, nil
}

// ToolStatus is one row of `ps2hdd doctor`.
type ToolStatus struct {
	Name     string `json:"name"`
	Path     string `json:"path,omitempty"`
	Present  bool   `json:"present"`
	Required bool   `json:"required"`
	Purpose  string `json:"purpose"`
}

// Tools reports which external executables are available.
//
// Only writes need external tools: reading the library, the partition table
// and the drive status is all done natively, so a missing hdl_dump degrades
// ps2hdd to a read-only browser rather than breaking it.
func (s *Services) Tools() []ToolStatus {
	defs := []struct {
		name, purpose string
		required      bool
	}{
		{"lsblk", "enumerating block devices for `detect`", true},
		{external.HDLDumpTool, "installing and removing PS2 games", false},
		{external.PFSFuseTool, "mounting +OPL and __.POPS", false},
		{external.FusermountTool, "unmounting PFS partitions", false},
		{external.PFSShellTool, "cross-checking the partition list", false},
	}
	out := make([]ToolStatus, 0, len(defs))
	for _, d := range defs {
		path, ok := external.Available(s.Runner, d.name)
		out = append(out, ToolStatus{
			Name: d.name, Path: path, Present: ok, Required: d.required, Purpose: d.purpose,
		})
	}
	return out
}

// SourceDirStatus reports whether a configured source directory is usable.
type SourceDirStatus struct {
	Platform model.Platform `json:"platform"`
	Path     string         `json:"path"`
	OK       bool           `json:"ok"`
	Reason   string         `json:"reason,omitempty"`
}

// SourceDirs checks the configured source directories.
func (s *Services) SourceDirs() []SourceDirStatus {
	out := []SourceDirStatus{}
	for _, d := range []struct {
		p    model.Platform
		path string
	}{{model.PlatformPS2, s.Config.Sources.PS2}, {model.PlatformPS1, s.Config.Sources.PS1}} {
		st := SourceDirStatus{Platform: d.p, Path: d.path}
		switch {
		case d.path == "":
			st.Reason = "not configured"
		default:
			fi, err := os.Stat(d.path)
			switch {
			case err != nil:
				st.Reason = err.Error()
			case !fi.IsDir():
				st.Reason = "not a directory"
			default:
				st.OK = true
			}
		}
		out = append(out, st)
	}
	return out
}
