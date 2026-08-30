package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
	"github.com/casmith/ps2hdd/internal/platform/ps2"
)

// Stage names the phase of a long operation, so the TUI can say what is
// happening and the CLI can log it.
type Stage string

const (
	StageInspecting Stage = "inspecting"
	StageValidating Stage = "validating"
	StageConverting Stage = "converting"
	StageExtracting Stage = "extracting"
	StageInstalling Stage = "installing"
	StageRemoving   Stage = "removing"
	StageVerifying  Stage = "verifying"
	StageSyncAssets Stage = "syncing_assets"
	StageComplete   Stage = "complete"
)

// Progress is one report from a long operation.
//
// Fraction is negative when the underlying tool gives no percentage. Callers
// must show an indeterminate spinner in that case rather than invent a number.
type Progress struct {
	Stage    Stage
	Fraction float64
	Message  string
}

// Indeterminate reports whether the progress fraction is unknown.
func (p Progress) Indeterminate() bool { return p.Fraction < 0 }

// ProgressFunc receives progress reports.
type ProgressFunc func(Progress)

func (f ProgressFunc) report(stage Stage, frac float64, msg string) {
	if f != nil {
		f(Progress{Stage: stage, Fraction: frac, Message: msg})
	}
}

// InstallOptions tune an install.
type InstallOptions struct {
	// Title overrides the name shown in OPL and the HDD browser.
	Title string
	// Hidden hides a PS2 game from the PS2 HDD browser.
	Hidden bool
	// SyncAssets fetches artwork after a successful install.
	SyncAssets bool
	// OnProgress receives stage and progress updates.
	OnProgress ProgressFunc
}

// InstallReport describes what an install did, or would do under --dry-run.
type InstallReport struct {
	Game model.Game `json:"game"`
	// Commands lists the external command lines that were run, or that would
	// be run under a dry run. Showing them is what makes --dry-run trustworthy.
	Commands [][]string `json:"commands,omitempty"`
	// Files lists paths written on the HDD, for the PS1 path.
	Files []string `json:"files,omitempty"`
	// AssetsInstalled counts artwork files copied after the install.
	AssetsInstalled int  `json:"assets_installed,omitempty"`
	DryRun          bool `json:"dry_run,omitempty"`
}

// InspectSource identifies an arbitrary image path and returns the title it
// describes, without touching the HDD.
func (s *Services) InspectSource(path string) (model.Game, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return model.Game{}, err
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return model.Game{}, err
	}
	if fi.IsDir() {
		return model.Game{}, fmt.Errorf("%s is a directory; name a disc image or a cuesheet", path)
	}

	ext := strings.ToLower(filepath.Ext(abs))
	// A cuesheet is unambiguously a PS1 rip. Everything else is tried as a PS2
	// image first and as a PS1 image second, since a bare .bin or .iso could
	// be either and only the disc's own boot record settles it.
	if ext == ".cue" {
		d, err := ps1.Inspect(abs)
		if err != nil {
			return model.Game{}, err
		}
		return ps1.GroupExplicit([]ps1.Disc{d}, ""), nil
	}
	if img, err := ps2.Inspect(abs); err == nil {
		return img.Game(), nil
	} else if !errors.Is(err, ps2.ErrNotPS2) {
		return model.Game{}, err
	}
	d, err := ps1.Inspect(abs)
	if err != nil {
		return model.Game{}, fmt.Errorf("%s is neither a PlayStation 2 nor a PlayStation 1 disc image: %w", filepath.Base(abs), err)
	}
	return ps1.GroupExplicit([]ps1.Disc{d}, ""), nil
}

// InspectSources identifies several paths as one logical title. Naming more
// than one path only makes sense for a multi-disc PS1 release.
func (s *Services) InspectSources(paths []string, title string) (model.Game, error) {
	if len(paths) == 0 {
		return model.Game{}, fmt.Errorf("no image given")
	}
	if len(paths) == 1 {
		g, err := s.InspectSource(paths[0])
		if err != nil {
			return g, err
		}
		if title != "" {
			g.Title = title
		}
		return g, nil
	}
	var discs []ps1.Disc
	for _, p := range paths {
		abs, err := filepath.Abs(p)
		if err != nil {
			return model.Game{}, err
		}
		d, err := ps1.Inspect(abs)
		if err != nil {
			return model.Game{}, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		discs = append(discs, d)
	}
	return ps1.GroupExplicit(discs, title), nil
}

// Install installs a title onto the HDD.
//
// The device is revalidated immediately before any write, regardless of what
// an earlier read established: a disk can be unplugged, or a by-id link
// repointed, between listing the library and modifying it.
func (s *Services) Install(ctx context.Context, g model.Game, opts InstallOptions) (InstallReport, error) {
	if opts.Title != "" {
		g.Title = opts.Title
	}
	switch g.Platform {
	case model.PlatformPS2:
		return s.installPS2(ctx, g, opts)
	case model.PlatformPS1:
		return s.installPS1(ctx, g, opts)
	default:
		return InstallReport{}, fmt.Errorf("unsupported platform %q", g.Platform)
	}
}

func (s *Services) installPS2(ctx context.Context, g model.Game, opts InstallOptions) (InstallReport, error) {
	rep := InstallReport{Game: g, DryRun: s.DryRun}
	log := logging.ContextLogger(ctx)
	opts.OnProgress.report(StageValidating, -1, "checking the HDD")

	t, err := s.Target(ctx, true)
	if err != nil {
		return rep, err
	}
	if err := s.ensureNotInstalled(ctx, g); err != nil {
		return rep, err
	}
	if err := s.ensureSpace(ctx, g); err != nil {
		return rep, err
	}
	if g.Media == model.MediaUnknown {
		return rep, fmt.Errorf("the media type of %s is unknown; ps2hdd will not guess between a CD and a DVD image", g.Title)
	}

	// An archived source has to become a real file before hdl_dump can read
	// it: hdl_dump seeks around the image, so a pipe is not an option.
	// A loose .bin has the same problem an archived one does: hdl_dump needs
	// the cuesheet beside it, not the raw image.
	source := HDLSourcePath(g.SourcePath)
	if g.ArchiveMember != "" {
		if s.DryRun {
			// Listing an archive reads its header only, so the plan shown here
			// is the plan that will run, cuesheet and all, rather than an
			// approximation of it.
			rep.Commands = append(rep.Commands,
				append([]string{external.SevenZipTool},
					external.ExtractArgs(g.SourcePath, g.ArchiveMember, "<scratch>")...))
			source = filepath.Join("<scratch>", filepath.Base(g.ArchiveMember))

			a := external.Archive{Runner: s.Runner}
			if entries, lerr := a.List(ctx, g.SourcePath); lerr == nil {
				if cue := cueMemberFor(entries, g.ArchiveMember); cue != "" {
					rep.Commands = append(rep.Commands,
						append([]string{external.SevenZipTool},
							external.ExtractArgs(g.SourcePath, cue, "<scratch>")...))
					source = filepath.Join("<scratch>", filepath.Base(cue))
				}
			}
		} else {
			extracted, cleanup, err := s.extractSource(ctx, g, opts)
			if err != nil {
				return rep, err
			}
			// The scratch copy is removed whatever happens next, including a
			// failed install: it is a duplicate of data that still exists in
			// the archive, and gigabytes of it.
			defer cleanup()
			source = extracted
		}
	}

	req := external.InstallRequest{
		Device:  t.Path,
		Name:    g.Title,
		Source:  source,
		Startup: model.OPLGameID(g.GameID),
		Media:   g.Media,
		Hidden:  opts.Hidden,
		OnProgress: func(frac float64, _ string) {
			opts.OnProgress.report(StageInstalling, frac, g.Title)
		},
	}
	args, err := external.InstallArgs(req)
	if err != nil {
		return rep, err
	}
	rep.Commands = append(rep.Commands, append([]string{external.HDLDumpTool}, args...))
	if s.DryRun {
		return rep, nil
	}

	unlock := s.LockHDD()
	defer unlock()

	opts.OnProgress.report(StageInstalling, -1, g.Title)
	if err := s.HDL.Install(ctx, req); err != nil {
		return rep, err
	}
	log.Info("installed PS2 game", "title", g.Title, "id", g.GameID, "device", t.Configured)

	if s.Config.Install.VerifyAfterInstall {
		opts.OnProgress.report(StageVerifying, -1, g.Title)
		if err := s.verifyPS2Installed(ctx, g); err != nil {
			return rep, err
		}
	}
	if opts.SyncAssets {
		opts.OnProgress.report(StageSyncAssets, -1, g.Title)
		n, err := s.syncAssetsFor(ctx, []model.Game{g})
		if err != nil {
			// Artwork is cosmetic; a failure must not undo a successful
			// install or be reported as one.
			log.Warn("artwork sync after install failed", "title", g.Title, "err", err)
		}
		rep.AssetsInstalled = n
	}
	opts.OnProgress.report(StageComplete, 1, g.Title)
	return rep, nil
}

// verifyPS2Installed re-reads the APA table and confirms the game is there.
func (s *Services) verifyPS2Installed(ctx context.Context, g model.Game) error {
	games, err := s.InstalledPS2(ctx)
	if err != nil {
		return fmt.Errorf("verify install: %w", err)
	}
	want := model.NormalizeGameID(g.GameID)
	for _, got := range games {
		if model.NormalizeGameID(got.GameID) == want {
			return nil
		}
	}
	return fmt.Errorf("verification failed: %s (%s) is not in the HDD's partition table after installing", g.Title, g.GameID)
}

func (s *Services) ensureNotInstalled(ctx context.Context, g model.Game) error {
	installed, err := s.Installed(ctx)
	if err != nil {
		// If the installed list cannot be read the install must not proceed:
		// writing a duplicate partition is worse than refusing.
		return fmt.Errorf("could not read the installed library: %w", err)
	}
	want := g.Key()
	for _, got := range installed {
		if got.Key() == want {
			return fmt.Errorf("%w: %s (%s)", ErrAlreadyInstalled, got.Title, got.GameID)
		}
	}
	return nil
}

func (s *Services) ensureSpace(ctx context.Context, g model.Game) error {
	free, err := s.FreeSpace(ctx)
	if err != nil {
		return err
	}
	// APA allocates in 128 MiB chunks, so the real cost is rounded up.
	const chunk = int64(apa.ChunkMB) * 1024 * 1024
	needed := ((g.SizeBytes + chunk - 1) / chunk) * chunk
	if needed > free {
		return &InsufficientSpaceError{Title: g.Title, Needed: needed, Free: free}
	}
	return nil
}

// installPS1 converts each disc to VCD and copies it into __.POPS.
func (s *Services) installPS1(ctx context.Context, g model.Game, opts InstallOptions) (InstallReport, error) {
	rep := InstallReport{Game: g, DryRun: s.DryRun}
	log := logging.ContextLogger(ctx)
	opts.OnProgress.report(StageValidating, -1, "checking PS1 support")

	if _, err := s.Target(ctx, true); err != nil {
		return rep, err
	}
	ready, err := s.PS1Readiness(ctx)
	if err != nil {
		return rep, err
	}
	if !ready.POPSPartition {
		return rep, fmt.Errorf("%w: the %s partition does not exist", ErrNotReady, ps1.POPSPartition)
	}
	// The runtime being absent stops the game booting, not the install. The
	// files can be imported afterwards, so this is a warning, not a refusal.
	if ready.RuntimeChecked && len(ready.Missing) > 0 {
		log.Warn("installing a PS1 game while the POPS runtime is incomplete", "missing", ready.Missing)
	}
	if err := s.ensureNotInstalled(ctx, g); err != nil {
		return rep, err
	}
	if err := s.ensureSpace(ctx, g); err != nil {
		return rep, err
	}

	total := len(g.Discs)
	if total == 0 {
		return rep, fmt.Errorf("%s has no discs to install", g.Title)
	}
	names := make([]string, total)
	for i, d := range g.Discs {
		names[i] = ps1.VCDName(d.GameID, g.Title, d.Number, total)
		rep.Files = append(rep.Files, ps1.POPSPartition+"/"+names[i])
	}
	if total > 1 {
		dir := ps1.DiscsDirName(g.Discs[0].GameID, g.Title)
		rep.Files = append(rep.Files, ps1.POPSPartition+"/"+dir+"/"+ps1.DiscsFile)
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

	// Conversion writes a multi-hundred-megabyte VCD, so it happens in the
	// cache directory and the result is copied in. Converting straight onto a
	// FUSE mount would be slower and would leave a partial file on the HDD if
	// it were interrupted.
	staging, err := os.MkdirTemp("", "ps2hdd-vcd-")
	if err != nil {
		return rep, err
	}
	defer os.RemoveAll(staging)

	err = m.With(ctx, ps1.POPSPartition, func(mp string) error {
		for i, d := range g.Discs {
			if err := ctx.Err(); err != nil {
				return err
			}
			label := g.Title
			if total > 1 {
				label = fmt.Sprintf("%s (disc %d of %d)", g.Title, d.Number, total)
			}
			opts.OnProgress.report(StageConverting, discFraction(i, total, 0), label)

			tmp := filepath.Join(staging, names[i])
			if err := ps1.Convert(d.SourcePath, tmp, ps1.ConvertOptions{
				OnProgress: func(f float64) {
					opts.OnProgress.report(StageConverting, discFraction(i, total, f), label)
				},
			}); err != nil {
				return fmt.Errorf("convert %s: %w", filepath.Base(d.SourcePath), err)
			}

			opts.OnProgress.report(StageInstalling, discFraction(i, total, 1), label)
			dest := filepath.Join(mp, names[i])
			if _, err := os.Stat(dest); err == nil {
				return fmt.Errorf("%s already exists in %s; remove it first", names[i], ps1.POPSPartition)
			}
			if err := copyFile(tmp, dest); err != nil {
				return fmt.Errorf("copy %s into %s: %w", names[i], ps1.POPSPartition, err)
			}
			_ = os.Remove(tmp)
		}

		// POPStarter reads DISCS.TXT to offer in-game disc swapping. It lives
		// in a directory named after the title's base VCD name.
		if total > 1 {
			dir := filepath.Join(mp, ps1.DiscsDirName(g.Discs[0].GameID, g.Title))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create the disc-swap directory: %w", err)
			}
			body := ps1.DiscsFileContents(names)
			if err := os.WriteFile(filepath.Join(dir, ps1.DiscsFile), []byte(body), 0o644); err != nil {
				return fmt.Errorf("write %s: %w", ps1.DiscsFile, err)
			}
		}
		return nil
	})
	if err != nil {
		return rep, err
	}
	log.Info("installed PS1 game", "title", g.Title, "discs", total)

	if s.Config.Install.VerifyAfterInstall {
		opts.OnProgress.report(StageVerifying, -1, g.Title)
		if err := s.verifyPS1Installed(ctx, names); err != nil {
			return rep, err
		}
	}
	if opts.SyncAssets {
		opts.OnProgress.report(StageSyncAssets, -1, g.Title)
		n, err := s.syncAssetsFor(ctx, []model.Game{g})
		if err != nil {
			log.Warn("artwork sync after install failed", "title", g.Title, "err", err)
		}
		rep.AssetsInstalled = n
	}
	opts.OnProgress.report(StageComplete, 1, g.Title)
	return rep, nil
}

// discFraction maps a per-disc fraction onto the whole title's progress.
func discFraction(index, total int, frac float64) float64 {
	if total <= 0 {
		return frac
	}
	return (float64(index) + frac) / float64(total)
}

func (s *Services) verifyPS1Installed(ctx context.Context, names []string) error {
	m, err := s.Mounts(ctx)
	if err != nil {
		return err
	}
	return m.With(ctx, ps1.POPSPartition, func(mp string) error {
		for _, n := range names {
			fi, err := os.Stat(filepath.Join(mp, n))
			if err != nil {
				return fmt.Errorf("verification failed: %s is not in %s after installing", n, ps1.POPSPartition)
			}
			if fi.Size() <= ps1.HeaderSize {
				return fmt.Errorf("verification failed: %s is %d bytes, which is only the POPS header", n, fi.Size())
			}
		}
		return nil
	})
}

// syncAssetsFor fetches artwork for a set of games and reports how many files
// were installed.
func (s *Services) syncAssetsFor(ctx context.Context, games []model.Game) (int, error) {
	mgr, err := s.AssetManager(false)
	if err != nil {
		return 0, err
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return 0, err
	}
	var installed int
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		inv, err := asset.Scan(mp)
		if err != nil {
			return err
		}
		plan, err := mgr.PlanSync(ctx, games, inv, mp)
		if err != nil {
			return err
		}
		res, err := mgr.Apply(ctx, plan, nil)
		if err != nil {
			return err
		}
		installed = len(res.Installed)
		return nil
	})
	return installed, err
}

// copyFile copies src to dest, removing a partial destination on failure.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.Create(dest)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(dest)
		return err
	}
	return nil
}
