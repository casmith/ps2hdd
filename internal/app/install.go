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
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/pfs"
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
	// Prefetch supplies titles unpacked ahead of this one. Nil means extract
	// inline, which is what a single install does.
	Prefetch *Prefetcher
	// Widescreen turns on POPStarter's GTE widescreen hack for a PS1 title by
	// writing $WIDESCREEN into its CHEATS.TXT. Off unless asked for: it does
	// not correct HUDs or 2D art, and some games do not survive it.
	Widescreen bool
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
	AssetsInstalled int `json:"assets_installed,omitempty"`
	// Warnings records what did not go fully to plan without failing the
	// install: a game on the disk that the console will not list is still
	// worth reporting, and is exactly the case that used to pass silently.
	Warnings []string `json:"warnings,omitempty"`
	DryRun   bool     `json:"dry_run,omitempty"`
}

// InspectSource identifies an arbitrary image path and returns the title it
// describes, without touching the HDD.
//
// An archive is read in place, exactly as a scan of a source directory reads
// one. Naming a .7z on the command line and picking the same file out of the
// library ought to reach the same code, and for a while they did not: the
// scanner knew about archives and this did not, so `install game.7z` reported
// a file that plainly is a game as "neither a PlayStation 2 nor a PlayStation
// 1 disc image".
func (s *Services) InspectSource(ctx context.Context, path string) (model.Game, error) {
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
	if external.IsArchive(abs) {
		return s.inspectArchivedSource(ctx, abs)
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

// inspectArchivedSource identifies the disc image inside an archive without
// unpacking it, which is what the source scanner does with the same file.
func (s *Services) inspectArchivedSource(ctx context.Context, abs string) (model.Game, error) {
	if _, err := s.Runner.Look(external.SevenZipTool); err != nil {
		return model.Game{}, fmt.Errorf("%s is an archive and 7z is not installed; run `ps2hdd doctor` for the command that installs it", filepath.Base(abs))
	}
	a := external.Archive{Runner: s.Runner}
	// PS2 first, then PS1: one archive holds one disc image, and only the
	// image's own boot record settles which console it is for.
	g, _, err := catalog.InspectArchivedPS2(ctx, a, abs)
	if err == nil {
		return g, nil
	}
	ps2Err := err
	g, err = catalog.InspectArchivedPS1(ctx, a, abs)
	if err == nil {
		return g, nil
	}
	return model.Game{}, fmt.Errorf("%s holds neither a PlayStation 2 nor a PlayStation 1 disc image: %w",
		filepath.Base(abs), ps2Err)
}

// InspectSources identifies several paths as one logical title. Naming more
// than one path only makes sense for a multi-disc PS1 release.
func (s *Services) InspectSources(ctx context.Context, paths []string, title string) (model.Game, error) {
	if len(paths) == 0 {
		return model.Game{}, fmt.Errorf("no image given")
	}
	if len(paths) == 1 {
		g, err := s.InspectSource(ctx, paths[0])
		if err != nil {
			return g, err
		}
		if title != "" {
			g.Title = title
		}
		return g, nil
	}
	// A multi-disc release with each disc in its own archive is grouped the
	// same way as one with each disc loose, so every path goes through
	// InspectSource and the discs are folded back together afterwards.
	var discs []ps1.Disc
	for _, p := range paths {
		g, err := s.InspectSource(ctx, p)
		if err != nil {
			return model.Game{}, fmt.Errorf("%s: %w", filepath.Base(p), err)
		}
		if g.Platform != model.PlatformPS1 {
			return model.Game{}, fmt.Errorf("%s is a PlayStation 2 image; only PlayStation 1 releases have several discs", filepath.Base(p))
		}
		discs = append(discs, gameDiscs(g)...)
	}
	return ps1.GroupExplicit(discs, title), nil
}

// gameDiscs turns a one-disc Game back into the ps1.Disc grouping wants.
func gameDiscs(g model.Game) []ps1.Disc {
	out := make([]ps1.Disc, 0, len(g.Discs))
	for _, d := range g.Discs {
		disc := ps1.Disc{
			GameID:        d.GameID,
			Title:         d.Title,
			SizeBytes:     d.SizeBytes,
			VCDBytes:      d.InstallSizeBytes,
			DiscNumber:    d.Number,
			ArchivePath:   g.SourcePath,
			ArchiveMember: d.ArchiveMember,
		}
		if d.ArchiveMember == "" {
			disc.ArchivePath = ""
			if filepath.Ext(strings.ToLower(d.SourcePath)) == ".cue" {
				disc.CuePath = d.SourcePath
			} else {
				disc.BinPath = d.SourcePath
			}
		}
		out = append(out, disc)
	}
	return out
}

// Install installs a title onto the HDD.
//
// The device is revalidated immediately before any write, regardless of what
// an earlier read established: a disk can be unplugged, or a by-id link
// repointed, between listing the library and modifying it.
func (s *Services) Install(ctx context.Context, g model.Game, opts InstallOptions) (InstallReport, error) {
	// An explicit --title always wins. Failing that, the serial on the disc is
	// a better source for the name than the filename is: a rip may be called
	// "hot-shots-golf-u-scus-94188" or "SLUS_00067" or "Disc 1", and none of
	// those is what the game is called. The name reached here is the one that
	// goes into the VCD's filename, the launcher's, and OPL's menu.
	switch {
	case opts.Title != "":
		g.Title = opts.Title
	default:
		if t, ok := s.CanonicalTitle(ctx, g); ok {
			g.Title = t
		}
	}
	// Space abandoned by a run that was killed is reclaimed here, at the point
	// it is about to be needed, rather than on a timer.
	if !s.DryRun {
		s.ReapStaleScratch(ctx)
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
	// Both pre-write checks are questions about the partition table, asked
	// microseconds apart about a disk that cannot change between them, so they
	// share one read of it. Reading it twice was a round trip per partition
	// twice over, on a bus where the seek is the cost.
	toc, installed, err := s.readPS2Table(t)
	if err != nil {
		return rep, err
	}
	if err := ensureNotInstalledIn(installed, g); err != nil {
		return rep, err
	}
	if err := ensurePS2SpaceIn(ctx, toc, g); err != nil {
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
		} else if pre, ok := s.takePrefetched(ctx, g, opts); ok {
			// Unpacked while the previous title was being written.
			defer pre.Release()
			source = pre.Path
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

// ensureNotInstalled refuses a title that is already on the drive.
//
// It reads only the platform being installed. Game.Key is prefixed with the
// platform, so a PS2 title can never match a PS1 entry and looking at the other
// library is a comparison that cannot succeed -- which mattered because the
// PS1 half means mounting __.POPS over pfsfuse. Every PS2 install was spawning
// a FUSE mount, scanning a partition and unmounting it to answer a question
// about the APA table.
func (s *Services) ensureNotInstalled(ctx context.Context, g model.Game) error {
	var installed []model.Game
	var err error
	if g.Platform == model.PlatformPS1 {
		installed, err = s.InstalledPS1(ctx)
	} else {
		installed, err = s.InstalledPS2(ctx)
	}
	if err != nil {
		// If the installed list cannot be read the install must not proceed:
		// writing a duplicate partition is worse than refusing.
		return fmt.Errorf("could not read the installed library: %w", err)
	}
	return ensureNotInstalledIn(installed, g)
}

// ensureNotInstalledIn is the comparison itself, against a list the caller has
// already read.
func ensureNotInstalledIn(installed []model.Game, g model.Game) error {
	want := g.Key()
	for _, got := range installed {
		if got.Key() == want {
			return fmt.Errorf("%w: %s (%s)", ErrAlreadyInstalled, got.Title, got.GameID)
		}
	}
	return nil
}

// readPS2Table reads the partition table once and returns both what a caller
// needs from it: the table for the space check, and the games for the
// duplicate check.
func (s *Services) readPS2Table(t *drive.Target) (*apa.TOC, []model.Game, error) {
	f, err := os.Open(t.Path)
	if err != nil {
		return nil, nil, err
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, t.SizeBytes)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the partition table: %w", err)
	}
	infos, err := apa.ReadGames(f, toc)
	if err != nil {
		return nil, nil, fmt.Errorf("could not read the installed library: %w", err)
	}
	return toc, catalog.PS2GamesFrom(infos), nil
}

// ensurePS2Space checks a title against the drive's unallocated APA chunks.
//
// What a title costs is decided by hdl_dump, and it is neither the image's
// size nor the image rounded up to a chunk: partition overhead is charged on
// top and can take another whole one. Rather than approximate that, the real
// partition table is read and the allocation is replayed against it.
func ensurePS2SpaceIn(ctx context.Context, toc *apa.TOC, g model.Game) error {
	needed, ok := toc.AllocationFor(g.SizeBytes)
	if !ok {
		_, _, free := toc.Chunks()
		return &InsufficientSpaceError{
			Title:  g.Title,
			Needed: apa.MaxAllocationFor(g.SizeBytes),
			Free:   int64(free) * int64(apa.ChunkMB) * 1024 * 1024,
			Where:  "the APA partition table",
		}
	}
	logging.ContextLogger(ctx).Debug("space check",
		"title", g.Title, "image", g.SizeBytes, "allocation", needed)
	return nil
}

// ensurePS1Space checks a title against the room left inside __.POPS.
//
// Not against the drive's unallocated chunks: a VCD is a file inside a
// partition that already exists, so free APA space is the wrong quantity in
// both directions. It says yes when __.POPS is full, and no when __.POPS has
// plenty of room but the drive has been fully partitioned -- which is the
// normal end state of a drive somebody has finished setting up.
func (s *Services) ensurePS1Space(ctx context.Context, g model.Game) error {
	needed := g.InstallSize()
	if needed <= 0 {
		return nil
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return err
	}
	var free int64
	if err := m.With(ctx, ps1.POPSPartition, func(mp string) error {
		free, err = freeSpace(mp)
		return err
	}); err != nil {
		return err
	}
	// pfsfuse reports the partition's real free zones through statfs, but a
	// build that does not is indistinguishable from a full partition. Zero is
	// therefore read as "no answer" and the install is allowed to proceed and
	// fail honestly, rather than being refused on a number that means nothing.
	if free <= 0 {
		logging.ContextLogger(ctx).Warn("could not measure free space in "+ps1.POPSPartition,
			"title", g.Title)
		return nil
	}
	if needed > free {
		return &InsufficientSpaceError{
			Title:  g.Title,
			Needed: needed,
			Free:   free,
			Where:  ps1.POPSPartition,
		}
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
	if err := s.ensurePS1Space(ctx, g); err != nil {
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
	// Per-game files go in __common/POPS/<vcd base>/, one directory per disc.
	// Not beside the VCD in __.POPS: both partitions have a POPS-shaped
	// directory and only one of them is read.
	support := func(i int, name string) string {
		return ps1.CommonPartition + "/" + ps1.POPSDir + "/" + ps1.GameDirName(names[i]) + "/" + name
	}
	if total > 1 {
		for i := range names {
			rep.Files = append(rep.Files, support(i, ps1.DiscsFile))
			// Disc 1 owns the memory card; the rest are pointed at it.
			if i > 0 {
				rep.Files = append(rep.Files, support(i, ps1.VMCDirFile))
			}
		}
	}
	if opts.Widescreen {
		for i := range names {
			rep.Files = append(rep.Files, support(i, ps1.CheatsFile))
		}
	}

	// The VCD alone is not a playable game: OPL has no PS1 support at all, so
	// without a launcher in +OPL/APPS the title exists on the disk and appears
	// in no menu. See internal/platform/ps1/launcher.go.
	//
	// A multi-disc title gets one launcher, pointing at disc 1; POPStarter
	// swaps to the rest through DISCS.TXT.
	launcherELF := ps1.LauncherELFName(names[0])
	launcherDir := drive.PartitionOPL + "/" + ps1.AppsDir + "/" + ps1.LauncherDirName(names[0])
	// When the runtime could not be inspected, "missing" and "unknown" are the
	// same value, so the launcher is planned and skipped later if it turns out
	// there was nothing to copy.
	wantLauncher := !ready.RuntimeChecked || ready.Runtime[ps1.POPStarterELF]
	if wantLauncher {
		rep.Files = append(rep.Files, launcherDir+"/"+launcherELF, launcherDir+"/"+ps1.TitleConfigFile)
	} else {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"%s is not in %s/%s, so no launcher was written and OPL will not list this game. "+
				"Import the runtime with `ps2hdd setup ps1 --import <dir>`, then run "+
				"`ps2hdd setup ps1 --launchers`; the game itself does not need reinstalling.",
			ps1.POPStarterELF, ps1.CommonPartition, ps1.POPSDir))
	}
	if total > ps1.MaxDiscsInDiscsFile {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"%s has %d discs, and DISCS.TXT describes at most %d. Every disc is installed and "+
				"each will boot from its own launcher, but the in-game disc-swap menu will not "+
				"work for this title.", g.Title, total, ps1.MaxDiscsInDiscsFile))
	}
	if !ps1.BootNameFitsOPL(launcherELF) {
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"%q is longer than the 64 characters OPL allows for a boot filename, so the entry "+
				"will appear on its Apps page and do nothing. Launch it from wLaunchELF instead, "+
				"or reinstall under a shorter title.", launcherELF))
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
	// An archived rip has to become real files before it can be converted:
	// the converter reads the cuesheet and seeks around the data track.
	if g.ArchiveMember != "" {
		if pre, ok := s.takePrefetched(ctx, g, opts); ok {
			defer pre.Release()
			g.Discs = pre.Discs
		} else {
			discs, cleanup, err := s.extractPS1Source(ctx, g, opts)
			if err != nil {
				return rep, err
			}
			defer cleanup()
			g.Discs = discs
		}
	}

	// Conversion staging goes in the scratch directory, not the system
	// temporary one. /tmp is tmpfs on most distributions, and a VCD is the
	// whole disc: converting a full CD there puts 740 MB in RAM, which is
	// precisely what install.scratch_dir exists to let a user avoid.
	staging, err := s.vcdStaging(g)
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

		return nil
	})
	if err != nil {
		return rep, err
	}
	// The per-game files live in a different partition from the VCDs, so they
	// are written under their own mount rather than the one above.
	if err := s.writePS1Support(ctx, m, names, opts.Widescreen); err != nil {
		log.Warn("could not write the POPStarter support files", "title", g.Title, "err", err)
		rep.Warnings = append(rep.Warnings, fmt.Sprintf(
			"the game is installed but its POPStarter support files could not be written, "+
				"so disc swapping and shared saves will not work: %s", firstLine(err.Error())))
	}
	if wantLauncher {
		if err := s.installPS1Launcher(ctx, m, g, names[0], staging); err != nil {
			// The discs are already installed and are not made worse by this.
			// Failing the whole install here would strand them; saying so
			// plainly leaves a state the user can fix and rerun.
			log.Warn("could not write the POPStarter launcher", "title", g.Title, "err", err)
			rep.Warnings = append(rep.Warnings, fmt.Sprintf(
				"the game is installed but its launcher could not be written, so OPL will not "+
					"list it: %s", firstLine(err.Error())))
		}
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
		// The artwork a PS1 title needs on OPL's Apps page is named after its
		// launcher, which is named after the VCD -- so the names it was just
		// installed under have to be on the game before the sync sees it.
		// Without them the sync writes only the serial-named copies and the
		// Apps entry stays blank until something syncs again.
		synced := g
		synced.Discs = append([]model.Disc(nil), g.Discs...)
		for i := range synced.Discs {
			if i < len(names) {
				synced.Discs[i].InstalledName = names[i]
			}
		}
		n, err := s.syncAssetsFor(ctx, []model.Game{synced})
		if err != nil {
			log.Warn("artwork sync after install failed", "title", g.Title, "err", err)
		}
		rep.AssetsInstalled = n
	}
	opts.OnProgress.report(StageComplete, 1, g.Title)
	return rep, nil
}

// writePS1Support writes the per-game files POPStarter reads, in the support
// directory each disc gets under __common/POPS.
//
// Multi-disc titles need two of them. DISCS.TXT lists every VCD and goes in
// every disc's directory, which is what the in-game disc-swap menu is built
// from. VMCDIR.TXT goes in the second disc onward and names disc 1's VCD,
// because POPStarter otherwise gives each VCD its own virtual memory card and
// a save made on disc 1 is then invisible on disc 2 -- a three-disc RPG that
// loses your save at the disc change is technically installed and practically
// useless.
//
// Neither is written for a single-disc title, which needs neither.
func (s *Services) writePS1Support(ctx context.Context, m *drive.MountManager, names []string, widescreen bool) error {
	if len(names) < 2 && !widescreen {
		return nil
	}
	discs := ps1.DiscsFileContents(names)
	vmc := ps1.VMCDirContents(names[0])
	return m.With(ctx, ps1.CommonPartition, func(mp string) error {
		for i, name := range names {
			dir := filepath.Join(mp, ps1.POPSDir, ps1.GameDirName(name))
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return fmt.Errorf("create the support directory for %s: %w", name, err)
			}
			if len(names) > 1 {
				if err := writeFile(filepath.Join(dir, ps1.DiscsFile), []byte(discs)); err != nil {
					return fmt.Errorf("write %s: %w", ps1.DiscsFile, err)
				}
				if i > 0 {
					if err := writeFile(filepath.Join(dir, ps1.VMCDirFile), []byte(vmc)); err != nil {
						return fmt.Errorf("write %s: %w", ps1.VMCDirFile, err)
					}
				}
			}
			if widescreen {
				if err := addCheat(filepath.Join(dir, ps1.CheatsFile), ps1.Widescreen); err != nil {
					return err
				}
			}
		}
		logging.ContextLogger(ctx).Info("wrote POPStarter support files",
			"discs", len(names), "widescreen", widescreen)
		return nil
	})
}

// addCheat puts a directive in a CHEATS.TXT without disturbing what is already
// there.
//
// The file is the user's: it is where their own cheat codes and per-game
// tuning live, and an install that replaced it would throw those away. So an
// existing file is appended to, an existing directive is left alone, and only
// an absent file is created.
func addCheat(path, directive string) error {
	body, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read %s: %w", ps1.CheatsFile, err)
	}
	for _, line := range strings.Split(string(body), "\n") {
		if strings.EqualFold(strings.TrimSpace(strings.TrimSuffix(line, "\r")), directive) {
			return nil
		}
	}
	if len(body) > 0 && !strings.HasSuffix(string(body), "\n") {
		body = append(body, '\n')
	}
	body = append(body, []byte(directive+"\n")...)
	if err := writeFile(path, body); err != nil {
		return fmt.Errorf("write %s: %w", ps1.CheatsFile, err)
	}
	return nil
}

// installPS1Launcher writes the one thing that makes an installed PS1 title
// selectable on the console: a copy of POPSTARTER.ELF renamed after the VCD,
// in its own directory under +OPL/APPS, next to a title.cfg naming it.
//
// POPStarter finds its VCD by reading its own filename, and OPL finds the ELF
// by reading title.cfg, so neither file is optional and neither name is a
// choice. internal/platform/ps1/launcher.go has the details and the citations.
func (s *Services) installPS1Launcher(ctx context.Context, m *drive.MountManager, g model.Game, vcdName, staging string) error {
	local, err := fetchPOPStarter(ctx, m, staging)
	if err != nil {
		return err
	}
	defer os.Remove(local)
	return m.With(ctx, drive.PartitionOPL, func(mp string) error {
		return writeLauncher(ctx, mp, local, vcdName, g.Title)
	})
}

// fetchPOPStarter copies POPSTARTER.ELF off the HDD into staging.
//
// It is taken from the runtime already on the disk rather than carried by
// ps2hdd, which keeps one copy authoritative: whichever POPStarter build the
// user installed is the one every launcher runs. The round trip through
// staging is because __common and +OPL cannot be read and written through one
// mount -- the mount manager holds one mountpoint per partition.
func fetchPOPStarter(ctx context.Context, m *drive.MountManager, staging string) (string, error) {
	local := filepath.Join(staging, ps1.POPStarterELF)
	err := m.With(ctx, ps1.CommonPartition, func(mp string) error {
		return copyFile(filepath.Join(mp, ps1.POPSDir, ps1.POPStarterELF), local)
	})
	if err != nil {
		return "", fmt.Errorf("read %s from %s: %w", ps1.POPStarterELF, ps1.CommonPartition, err)
	}
	return local, nil
}

// writeLauncher creates one title's launcher directory inside a mounted +OPL.
func writeLauncher(ctx context.Context, oplMount, popstarter, vcdName, title string) error {
	elf := ps1.LauncherELFName(vcdName)
	dir := filepath.Join(oplMount, ps1.AppsDir, ps1.LauncherDirName(vcdName))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("create the launcher directory: %w", err)
	}
	if err := copyFile(popstarter, filepath.Join(dir, elf)); err != nil {
		return fmt.Errorf("copy %s: %w", elf, err)
	}
	body := ps1.TitleConfigContents(ps1.LauncherTitle(title), elf)
	if err := writeFile(filepath.Join(dir, ps1.TitleConfigFile), []byte(body)); err != nil {
		return fmt.Errorf("write %s: %w", ps1.TitleConfigFile, err)
	}
	logging.ContextLogger(ctx).Info("wrote POPStarter launcher",
		"title", title, "dir", ps1.LauncherDirName(vcdName), "boot", elf)
	return nil
}

// writeFile is os.WriteFile without the O_TRUNC. Every caller writes onto a
// PFS mount, where truncating an existing file fails; see internal/pfs.
func writeFile(path string, body []byte) error {
	f, err := pfs.Create(path, 0o644)
	if err != nil {
		return err
	}
	_, err = f.Write(body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(path)
	}
	return err
}

// firstLine keeps a warning to one line; a wrapped error chain reads badly
// inside a sentence.
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// vcdStaging makes a directory to convert into, in the scratch space, with
// room for the largest disc of the title checked before the work starts.
func (s *Services) vcdStaging(g model.Game) (string, error) {
	root, err := s.ScratchRoot()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", fmt.Errorf("create the scratch directory %s: %w", root, err)
	}
	// Discs are converted and copied one at a time, so the peak is the largest
	// of them rather than their total.
	var largest int64
	for _, d := range g.Discs {
		if n := ps1.VCDSize(d.SizeBytes, 0); n > largest {
			largest = n
		}
	}
	need := largest + scratchHeadroom
	if free, err := freeSpace(root); err == nil && free < need {
		return "", fmt.Errorf(
			"%s needs %s of scratch space to convert into %s, which has %s free.\n"+
				"Set install.scratch_dir to a directory with more room",
			g.Title, model.HumanSize(need), root, model.HumanSize(free))
	}
	return os.MkdirTemp(root, "vcd-")
}

// takePrefetched collects a title unpacked ahead, saying so while it waits.
//
// Waiting here is normal -- the installer has caught up with the pipeline -- but
// it can be a minute of decompression, and without this the last thing reported
// was the stage before it. A run sat on "checking the HDD" while it was in fact
// unpacking, which reads as a hang and was reported as one.
func (s *Services) takePrefetched(ctx context.Context, g model.Game, opts InstallOptions) (*PrefetchedSource, bool) {
	if opts.Prefetch == nil || g.ArchiveMember == "" {
		return nil, false
	}
	opts.OnProgress.report(StageExtracting, -1, g.Title)
	return opts.Prefetch.Take(ctx, g)
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
		// A PS1 title is an Apps entry, and OPL looks up an app's artwork by
		// its boot filename rather than by a serial. This is the second place
		// artwork is installed -- SyncAssets is the other -- and leaving it out
		// here meant a fresh install had no Apps-page art until something
		// synced again.
		_, err = mgr.EnsureAppArtwork(games, mp)
		return err
	})
	return installed, err
}

// copyFile copies src to dest, removing a partial destination on failure.
//
// Both callers write into a PFS partition mounted over FUSE -- the POPS
// runtime into __common, a converted VCD into __.POPS -- so the destination is
// replaced rather than truncated. See internal/pfs.
func copyFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := pfs.Create(dest, 0o644)
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
