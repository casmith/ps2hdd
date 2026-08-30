package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
)

// scratchHeadroom is spare space required beyond the image itself, so that
// filling the scratch filesystem exactly to the brim is not treated as
// success.
const scratchHeadroom = 256 << 20

// extractSource decompresses an archived image into scratch space and returns
// its path along with a cleanup function the caller must defer.
//
// hdl_dump seeks around the image it injects, so it needs a real file; there
// is no streaming a 4 GB rip straight from the archive into it. The copy is
// deleted afterwards either way, because it duplicates data the archive still
// holds.
func (s *Services) extractSource(ctx context.Context, g model.Game, opts InstallOptions) (string, func(), error) {
	log := logging.ContextLogger(ctx)
	a := external.Archive{Runner: s.Runner}
	if _, ok := a.Available(); !ok {
		return "", nil, &MissingToolError{
			Tool:    external.SevenZipTool,
			Feature: fmt.Sprintf("Installing %s, which is inside an archive", g.Title),
		}
	}

	root, err := s.ScratchRoot()
	if err != nil {
		return "", nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return "", nil, fmt.Errorf("create the scratch directory %s: %w", root, err)
	}
	// The size is checked before the work starts rather than after 4 GB of
	// decompression has already filled the disk.
	need := g.SizeBytes + scratchHeadroom
	if free, err := freeSpace(root); err == nil && free < need {
		return "", nil, fmt.Errorf(
			"%s needs %s of scratch space to unpack into %s, which has %s free.\n"+
				"Set install.scratch_dir to a directory with more room",
			g.Title, model.HumanSize(need), root, model.HumanSize(free))
	}

	dir, err := os.MkdirTemp(root, "extract-")
	if err != nil {
		return "", nil, fmt.Errorf("create a scratch directory in %s: %w", root, err)
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("could not remove the scratch directory", "dir", dir, "err", err)
		}
	}

	opts.OnProgress.report(StageExtracting, -1,
		fmt.Sprintf("unpacking %s", filepath.Base(g.ArchiveMember)))
	log.Info("extracting archived source", "archive", g.SourcePath,
		"member", g.ArchiveMember, "into", dir)

	path, err := a.Extract(ctx, g.SourcePath, g.ArchiveMember, dir)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("extract %s from %s: %w",
			g.ArchiveMember, filepath.Base(g.SourcePath), err)
	}
	// 7z reports success without writing anything if the member name does not
	// match, so the file is confirmed rather than assumed.
	fi, err := os.Stat(path)
	if err != nil {
		cleanup()
		return "", nil, fmt.Errorf("%s was not written by the extraction of %s",
			filepath.Base(path), filepath.Base(g.SourcePath))
	}
	log.Info("extracted archived source", "path", path, "bytes", fi.Size())
	return path, cleanup, nil
}

// ScratchRoot is where archived images are unpacked.
//
// It defaults to the cache directory rather than the system temporary
// directory: several distributions mount /tmp as tmpfs, and unpacking a
// dual-layer DVD rip into RAM is a good way to take a machine down.
func (s *Services) ScratchRoot() (string, error) {
	if dir := config.ExpandPath(s.Config.Install.ScratchDir); dir != "" {
		return dir, nil
	}
	cache, err := config.CacheDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(cache, "scratch"), nil
}
