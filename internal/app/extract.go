package app

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

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
	source := HDLSourcePath(path)
	if err := ensureCuesheet(source); err != nil {
		cleanup()
		return "", nil, err
	}
	return HDLSourcePath(path), cleanup, nil
}

// ensureCuesheet writes a cuesheet beside a raw CD rip that has none.
//
// hdl_dump identifies its input by probing, and a bare 2352-byte-per-sector
// .bin matches nothing it knows. Its ISO probe looks for "\001CD001" at
// 0x8000, which is sector 16 counted in 2048-byte sectors; in a MODE2/2352 rip
// that descriptor is at 0x9318 instead, twenty-four bytes into a wider sector.
// Its only other reader for this shape is the CDRWIN one, which needs a
// cuesheet. So the file is refused with "Input or output is unsupported" --
// exit 114 -- and a perfectly good rip cannot be installed.
//
// Plenty of archives hold exactly this: one .bin, no .cue. The cuesheet those
// rips are missing has no information in it that the file does not already
// carry, so it is written rather than demanded. hdl_dump accepts
// "TRACK 01 MODE2/2352" and reads the image correctly from there.
//
// Nothing is guessed. A size that is not a whole number of 2352-byte sectors
// is not this shape, and the descriptor is checked where a raw rip would keep
// it before anything is written.
func ensureCuesheet(source string) error {
	if !strings.EqualFold(filepath.Ext(source), ".bin") {
		return nil // already a cuesheet, or an ISO hdl_dump can read
	}
	raw, err := isRawMode2(source)
	if err != nil || !raw {
		// Not a raw rip, or unreadable: leave it alone and let hdl_dump say so
		// in its own words rather than inventing a cuesheet for it.
		return nil
	}
	cue := strings.TrimSuffix(source, filepath.Ext(source)) + ".cue"
	body := fmt.Sprintf("FILE %q BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n",
		filepath.Base(source))
	if err := os.WriteFile(cue, []byte(body), 0o600); err != nil {
		return fmt.Errorf("write a cuesheet for %s: %w", filepath.Base(source), err)
	}
	return nil
}

// isRawMode2 reports whether a file is a MODE2/2352 CD image: a whole number
// of 2352-byte sectors, with the ISO 9660 primary volume descriptor where such
// an image keeps it.
func isRawMode2(path string) (bool, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	if fi.Size()%rawSectorSize != 0 {
		return false, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return false, err
	}
	defer f.Close()
	// Sector 16, past the 24 bytes of sync, header and subheader.
	buf := make([]byte, 6)
	if _, err := f.ReadAt(buf, 16*rawSectorSize+rawSectorHeader); err != nil {
		return false, nil
	}
	return string(buf) == "\x01CD001", nil
}

const (
	// rawSectorSize is a CD sector as ripped, sync bytes and all.
	rawSectorSize = 2352
	// rawSectorHeader is the sync, header and subheader before the user data.
	rawSectorHeader = 24
)

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

// extractPS1Source unpacks an archived PS1 rip and returns discs pointing at
// the extracted files.
//
// Unlike a PS2 image, a PS1 rip is more than one file: the cuesheet names the
// data track, and the converter reads both. They are extracted together into
// the same directory so the bare filename in the FILE line resolves.
func (s *Services) extractPS1Source(ctx context.Context, g model.Game, opts InstallOptions) ([]model.Disc, func(), error) {
	log := logging.ContextLogger(ctx)
	a := external.Archive{Runner: s.Runner}
	if _, ok := a.Available(); !ok {
		return nil, nil, &MissingToolError{
			Tool:    external.SevenZipTool,
			Feature: fmt.Sprintf("Installing %s, which is inside an archive", g.Title),
		}
	}

	root, err := s.ScratchRoot()
	if err != nil {
		return nil, nil, err
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, nil, fmt.Errorf("create the scratch directory %s: %w", root, err)
	}
	// A PS1 disc plus the VCD it converts into both live here at once.
	need := g.SizeBytes*2 + scratchHeadroom
	if free, err := freeSpace(root); err == nil && free < need {
		return nil, nil, fmt.Errorf(
			"%s needs %s of scratch space to unpack and convert in %s, which has %s free.\n"+
				"Set install.scratch_dir to a directory with more room",
			g.Title, model.HumanSize(need), root, model.HumanSize(free))
	}

	dir, err := os.MkdirTemp(root, "ps1-")
	if err != nil {
		return nil, nil, err
	}
	cleanup := func() {
		if err := os.RemoveAll(dir); err != nil {
			log.Warn("could not remove the scratch directory", "dir", dir, "err", err)
		}
	}

	opts.OnProgress.report(StageExtracting, -1, fmt.Sprintf("unpacking %s", filepath.Base(g.SourcePath)))

	// Everything is taken out, not just the named member: a cuesheet is
	// useless without the track it names, and a rip's data files are the only
	// other things in these archives.
	if err := a.ExtractAll(ctx, g.SourcePath, dir); err != nil {
		cleanup()
		return nil, nil, fmt.Errorf("extract %s: %w", filepath.Base(g.SourcePath), err)
	}

	discs := append([]model.Disc(nil), g.Discs...)
	for i := range discs {
		member := discs[i].ArchiveMember
		if member == "" {
			member = g.ArchiveMember
		}
		p := filepath.Join(dir, filepath.Base(member))
		if _, err := os.Stat(p); err != nil {
			cleanup()
			return nil, nil, fmt.Errorf("%s was not written by the extraction of %s",
				filepath.Base(member), filepath.Base(g.SourcePath))
		}
		discs[i].SourcePath = p
		discs[i].ArchiveMember = ""
	}
	log.Info("extracted archived PS1 source", "archive", g.SourcePath, "into", dir)
	return discs, cleanup, nil
}

// Scratch left behind.
//
// Every extraction and conversion removes its own directory on the way out,
// including after a failed install. A deferred cleanup does not run when the
// process is killed, though -- an OOM kill, a power cut, a Ctrl-\ -- and a bulk
// run that dies partway leaves gigabytes behind in a cache directory nobody
// thinks to look in.
//
// Reaping is by age rather than by tracking what is live. A directory in use
// is minutes old; nothing legitimate is a day old, and installs do not run
// concurrently -- the drive is locked for one at a time. That makes the rule
// safe without any bookkeeping to go stale.

// staleScratchAge is how old a leftover must be before it is assumed abandoned.
const staleScratchAge = 24 * time.Hour

// scratchPrefixes are the directory names the extract and convert paths make.
var scratchPrefixes = []string{"extract-", "ps1-", "vcd-", "launcher-"}

// StaleScratch reports the abandoned scratch directories and their total size.
func (s *Services) StaleScratch() (dirs []string, bytes int64, err error) {
	root, err := s.ScratchRoot()
	if err != nil {
		return nil, 0, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		// No scratch directory is no leftovers, not a failure to look.
		if os.IsNotExist(err) {
			return nil, 0, nil
		}
		return nil, 0, err
	}
	cutoff := time.Now().Add(-staleScratchAge)
	for _, e := range entries {
		if !e.IsDir() || !isScratchDir(e.Name()) {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		dir := filepath.Join(root, e.Name())
		dirs = append(dirs, dir)
		bytes += dirSize(dir)
	}
	return dirs, bytes, nil
}

// ReapStaleScratch removes abandoned scratch directories and reports what it
// freed. It is called before an install rather than on a timer, because that
// is the moment the space is about to be needed.
func (s *Services) ReapStaleScratch(ctx context.Context) (int, int64) {
	dirs, bytes, err := s.StaleScratch()
	if err != nil || len(dirs) == 0 {
		return 0, 0
	}
	n := 0
	for _, d := range dirs {
		if err := os.RemoveAll(d); err != nil {
			logging.ContextLogger(ctx).Warn("could not remove abandoned scratch", "dir", d, "err", err)
			continue
		}
		n++
	}
	logging.ContextLogger(ctx).Info("removed abandoned scratch directories", "count", n, "bytes", bytes)
	return n, bytes
}

func isScratchDir(name string) bool {
	for _, p := range scratchPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// dirSize totals a directory tree, ignoring what it cannot read: this figure
// is reported to a human, not acted on.
func dirSize(root string) int64 {
	var total int64
	_ = filepath.WalkDir(root, func(_ string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		if info, err := d.Info(); err == nil {
			total += info.Size()
		}
		return nil
	})
	return total
}
