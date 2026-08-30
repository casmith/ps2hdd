package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

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

	entries, err := a.List(ctx, g.SourcePath)
	if err != nil {
		return "", nil, fmt.Errorf("list %s: %w", filepath.Base(g.SourcePath), err)
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

	// A raw MODE2/2352 rip needs its cuesheet: hdl_dump cannot read the .bin
	// alone. 7z's `e` flattens paths, so the cue lands beside the image and
	// the FILE line it carries resolves.
	if cue := cueMemberFor(entries, g.ArchiveMember); cue != "" {
		if _, err := a.Extract(ctx, g.SourcePath, cue, dir); err != nil {
			cleanup()
			return "", nil, fmt.Errorf("extract %s from %s: %w",
				cue, filepath.Base(g.SourcePath), err)
		}
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
	return HDLSourcePath(path), cleanup, nil
}

// cueMemberFor finds the cuesheet inside an archive that describes a member.
//
// A sheet named for the image is the match. Failing that, a lone cuesheet in
// an archive holding one image describes that image -- rip sets vary in
// whether the two names agree exactly. More than one, and nothing is assumed.
func cueMemberFor(entries []external.ArchiveEntry, member string) string {
	stem := strings.TrimSuffix(member, filepath.Ext(member))
	var cues []string
	for _, e := range entries {
		if !strings.EqualFold(filepath.Ext(e.Name), ".cue") {
			continue
		}
		if strings.EqualFold(strings.TrimSuffix(e.Name, filepath.Ext(e.Name)), stem) {
			return e.Name
		}
		cues = append(cues, e.Name)
	}
	if len(cues) == 1 {
		return cues[0]
	}
	return ""
}

// HDLSourcePath returns the path hdl_dump should be given for a disc image.
//
// hdl_dump cannot read a raw MODE2/2352 .bin on its own; its input layer
// answers "Input or output is unsupported". It reads the CDRWIN cuesheet that
// names the .bin perfectly well, and that sheet is what carries the sector
// layout. So when a cuesheet sits beside a .bin, that is the path to hand
// over. Anything else is passed through untouched.
func HDLSourcePath(image string) string {
	if !strings.EqualFold(filepath.Ext(image), ".bin") {
		return image
	}
	stem := strings.TrimSuffix(image, filepath.Ext(image))
	for _, ext := range []string{".cue", ".CUE"} {
		if fi, err := os.Stat(stem + ext); err == nil && fi.Mode().IsRegular() {
			return stem + ext
		}
	}
	return image
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
