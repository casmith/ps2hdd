package catalog

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"

	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
	"github.com/casmith/ps2hdd/internal/platform/ps2"
)

// maxScanDepth bounds directory recursion. Source trees are one or two levels
// deep in practice; the limit keeps a symlink loop or a mistakenly configured
// root from turning a scan into a filesystem crawl.
const maxScanDepth = 6

// ps2Extensions are the PS2 image extensions the scanner considers. Filenames
// are only ever used to decide what is worth opening; identity always comes
// from the image itself.
var ps2Extensions = map[string]bool{".iso": true, ".bin": true, ".img": true}

// ps2ScanExtensions is what the walk actually collects: loose images plus the
// archives that hold one. Identification happens the same way for both; only
// the reader differs.
var ps2ScanExtensions = func() map[string]bool {
	m := map[string]bool{}
	for e := range ps2Extensions {
		m[e] = true
	}
	for _, e := range []string{".7z", ".zip", ".rar"} {
		m[e] = true
	}
	return m
}()

// ps1Extensions are the PS1 entry points. A .cue takes precedence over the
// .bin it references, and a .bin named by a .cue is never listed separately.
var ps1Extensions = map[string]bool{".cue": true, ".bin": true, ".img": true, ".iso": true}

// ps1ScanExtensions is what the PS1 walk collects: loose entry points plus the
// archives that hold one. A PS1 library is very often entirely archived --
// 2,097 of 2,103 files in the collection this was built against -- so treating
// archives as invisible meant finding six titles in a library of two thousand.
var ps1ScanExtensions = func() map[string]bool {
	m := map[string]bool{}
	for e := range ps1Extensions {
		m[e] = true
	}
	for _, e := range []string{".7z", ".zip", ".rar"} {
		m[e] = true
	}
	return m
}()

// ScanResult is the outcome of scanning one source directory.
type ScanResult struct {
	Root  string       `json:"root"`
	Games []model.Game `json:"games"`
	// Problems lists files that looked like game images but could not be
	// identified, so a user can see why something is missing from the list
	// rather than wondering.
	Problems []ScanProblem `json:"problems,omitempty"`
	// Scanned and Cached count the files inspected and the ones served from
	// cache, which is what makes a rescan visibly faster.
	Scanned int `json:"scanned"`
	Cached  int `json:"cached"`
}

// ScanProblem is one unidentifiable file.
type ScanProblem struct {
	Path   string `json:"path"`
	Reason string `json:"reason"`
}

// ScanProgress reports how far a scan has got, so a caller can show something
// more useful than a spinner. A library of two thousand archives takes minutes
// to inspect, and without a position in it there is no way to tell a slow scan
// from a stuck one.
//
// Path advances in the alphabetical order the files were collected in and
// never goes backwards, even though up to Concurrency files are in flight at
// once: it names how far the scan has got, not what any one worker is doing.
type ScanProgress struct {
	// Root is the source directory being scanned.
	Root string
	// Done counts files inspected, Total the files to inspect.
	Done, Total int
	// Path is the furthest file reached, in collection order.
	Path string
	// Cached is true when Path was served from the scan cache rather than
	// opened, which is why a rescan runs so much faster.
	Cached bool
}

// Scanner walks configured source directories.
type Scanner struct {
	// Cache stores inspection results between runs.
	Cache *Cache
	// Concurrency bounds parallel inspection. Inspecting an image is I/O
	// bound, usually against a NAS, so a handful of readers helps and a
	// hundred does not.
	Concurrency int
	// Archive opens compressed sources. When its tool is not installed,
	// archives are reported as skipped rather than silently passed over.
	Archive external.Archive
	// OnProgress, when set, is called as each file is inspected. Calls are
	// serialised, so it does not need to be safe for concurrent use.
	OnProgress func(ScanProgress)
}

// NewScanner returns a scanner with sensible defaults.
func NewScanner(cache *Cache, runner external.Runner) *Scanner {
	n := runtime.NumCPU()
	if n > 4 {
		n = 4
	}
	return &Scanner{Cache: cache, Concurrency: n, Archive: external.Archive{Runner: runner}}
}

// ScanPS2 walks a directory for PS2 disc images.
func (s *Scanner) ScanPS2(ctx context.Context, root string) (ScanResult, error) {
	res := ScanResult{Root: root}
	if root == "" {
		return res, nil
	}
	files, err := s.collect(root, ps2ScanExtensions, nil)
	if err != nil {
		return res, err
	}

	_, haveArchiveTool := s.Archive.Available()
	outcomes := s.inspectAll(ctx, root, files, func(path string) (model.Game, error) {
		if external.IsArchive(path) {
			if !haveArchiveTool {
				return model.Game{}, fmt.Errorf("%s is an archive; install %s to read inside it",
					filepath.Base(path), external.SevenZipTool)
			}
			g, _, err := InspectArchivedPS2(ctx, s.Archive, path)
			return g, err
		}
		img, err := ps2.Inspect(path)
		if err != nil {
			return model.Game{}, err
		}
		return img.Game(), nil
	})
	for _, o := range outcomes {
		res.Scanned++
		if o.cached {
			res.Cached++
		}
		if o.err != "" {
			res.Problems = append(res.Problems, ScanProblem{Path: o.path, Reason: o.err})
			continue
		}
		res.Games = append(res.Games, o.game)
	}
	model.SortGames(res.Games)
	sort.Slice(res.Problems, func(i, j int) bool { return res.Problems[i].Path < res.Problems[j].Path })
	return res, nil
}

// ScanPS1 walks a directory for PS1 discs and groups them into logical titles.
func (s *Scanner) ScanPS1(ctx context.Context, root string) (ScanResult, error) {
	res := ScanResult{Root: root}
	if root == "" {
		return res, nil
	}
	// Files referenced by a cuesheet must not be listed in their own right:
	// "Disc 1.bin" is part of "Disc 1.cue", not a separate title.
	referenced, err := s.cueReferences(root)
	if err != nil {
		return res, err
	}
	files, err := s.collect(root, ps1ScanExtensions, referenced)
	if err != nil {
		return res, err
	}

	_, haveArchiveTool := s.Archive.Available()
	outcomes := s.inspectAll(ctx, root, files, func(path string) (model.Game, error) {
		if external.IsArchive(path) {
			if !haveArchiveTool {
				return model.Game{}, fmt.Errorf("%s is an archive; install %s to read inside it",
					filepath.Base(path), external.SevenZipTool)
			}
			return InspectArchivedPS1(ctx, s.Archive, path)
		}
		d, err := ps1.Inspect(path)
		if err != nil {
			return model.Game{}, err
		}
		// A single disc is carried through as a one-disc Game; Group below
		// folds the discs of a release back together.
		return model.Game{
			Platform:         model.PlatformPS1,
			Title:            d.Title,
			GameID:           d.GameID,
			SizeBytes:        d.SizeBytes,
			InstallSizeBytes: d.VCDBytes,
			SourcePath:       d.SourcePath(),
			Discs: []model.Disc{{
				Number:           d.DiscNumber,
				GameID:           d.GameID,
				Title:            d.Title,
				SourcePath:       d.SourcePath(),
				SizeBytes:        d.SizeBytes,
				InstallSizeBytes: d.VCDBytes,
			}},
		}, nil
	})

	var discs []ps1.Disc
	for _, o := range outcomes {
		res.Scanned++
		if o.cached {
			res.Cached++
		}
		if o.err != "" {
			res.Problems = append(res.Problems, ScanProblem{Path: o.path, Reason: o.err})
			continue
		}
		d := ps1.Disc{
			GameID:    o.game.GameID,
			Title:     o.game.Title,
			SizeBytes: o.game.SizeBytes,
		}
		if len(o.game.Discs) > 0 {
			d.DiscNumber = o.game.Discs[0].Number
		}
		switch {
		case o.game.ArchiveMember != "":
			// Grouping rebuilds games from discs, so the archive has to
			// travel on the disc or it is lost in the round trip and the
			// install path cannot know there is anything to extract.
			d.ArchivePath = o.path
			d.ArchiveMember = o.game.ArchiveMember
		case strings.EqualFold(filepath.Ext(o.path), ".cue"):
			d.CuePath = o.path
		default:
			d.BinPath = o.path
		}
		discs = append(discs, d)
	}
	res.Games = ps1.Group(discs, root)
	model.SortGames(res.Games)
	sort.Slice(res.Problems, func(i, j int) bool { return res.Problems[i].Path < res.Problems[j].Path })
	return res, nil
}

// inspectResult is one file's outcome.
type inspectResult struct {
	path   string
	game   model.Game
	err    string
	cached bool
}

// inspectAll inspects files with bounded concurrency, consulting the cache.
func (s *Scanner) inspectAll(ctx context.Context, root string, files []string, inspect func(string) (model.Game, error)) []inspectResult {
	log := logging.ContextLogger(ctx)
	out := make([]inspectResult, len(files))
	sem := make(chan struct{}, max(1, s.Concurrency))
	var wg sync.WaitGroup
	seen := make(map[string]bool, len(files))
	var seenMu sync.Mutex

	// Progress is reported from the worker goroutines, so the counter, the
	// furthest index reached and the callback itself are all covered by one
	// mutex: the callback then sees a consistent snapshot and does not have to
	// be thread-safe.
	var progMu sync.Mutex
	done, furthest := 0, -1
	report := func(i int, cached bool) {
		if s.OnProgress == nil {
			return
		}
		progMu.Lock()
		defer progMu.Unlock()
		done++
		if i > furthest {
			furthest = i
		}
		s.OnProgress(ScanProgress{
			Root: root, Done: done, Total: len(files),
			Path: files[furthest], Cached: cached,
		})
	}

	for i, path := range files {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(i int, path string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if ctx.Err() != nil {
				return
			}

			seenMu.Lock()
			seen[path] = true
			seenMu.Unlock()

			fi, err := os.Stat(path)
			if err != nil {
				out[i] = inspectResult{path: path, err: err.Error()}
				report(i, false)
				return
			}
			if s.Cache != nil {
				if e, ok := s.Cache.Get(path, fi); ok {
					out[i] = inspectResult{path: path, game: e.Game, err: e.Err, cached: true}
					report(i, true)
					return
				}
			}
			g, ierr := inspect(path)
			if s.Cache != nil {
				s.Cache.Put(path, fi, g, ierr)
			}
			r := inspectResult{path: path, game: g}
			if ierr != nil {
				r.err = ierr.Error()
				log.Debug("source file not identified", "path", path, "err", ierr)
			}
			out[i] = r
			report(i, false)
		}(i, path)
	}
	wg.Wait()

	if s.Cache != nil {
		s.Cache.Prune(seen)
		if err := s.Cache.Save(); err != nil {
			log.Warn("could not save the source scan cache", "err", err)
		}
	}

	// Drop slots for files skipped because the context was cancelled.
	kept := out[:0]
	for _, r := range out {
		if r.path != "" {
			kept = append(kept, r)
		}
	}
	return kept
}

// collect walks root and returns candidate files, skipping anything in the
// exclude set.
func (s *Scanner) collect(root string, exts map[string]bool, exclude map[string]bool) ([]string, error) {
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	fi, err := os.Stat(rootAbs)
	if err != nil {
		return nil, fmt.Errorf("source directory %s: %w", root, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("source path %s is not a directory", root)
	}

	var files []string
	err = filepath.WalkDir(rootAbs, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable subdirectory should not abort the whole scan.
			if d != nil && d.IsDir() {
				slog.Debug("skipping unreadable directory", "path", path, "err", err)
				return fs.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			if depthOf(rootAbs, path) > maxScanDepth {
				return fs.SkipDir
			}
			if strings.HasPrefix(d.Name(), ".") && path != rootAbs {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if strings.HasPrefix(d.Name(), ".") {
			return nil
		}
		if !exts[strings.ToLower(filepath.Ext(path))] {
			return nil
		}
		if exclude[path] {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil && !errors.Is(err, fs.SkipAll) {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// cueReferences returns the set of data files named by cuesheets under root.
func (s *Scanner) cueReferences(root string) (map[string]bool, error) {
	refs := map[string]bool{}
	cues, err := s.collect(root, map[string]bool{".cue": true}, nil)
	if err != nil {
		return nil, err
	}
	for _, cue := range cues {
		c, err := ps1.ParseCueFile(cue)
		if err != nil {
			continue // an unreadable sheet excludes nothing
		}
		// Every track the sheet names, not only the first. A split rip's
		// later tracks are audio: scanned on their own they have no volume
		// descriptor, so each one would be reported as "not a PlayStation 1
		// disc image" -- one spurious problem per track, for a title that is
		// listed correctly alongside them.
		for _, p := range c.FilePaths {
			if abs, err := filepath.Abs(p); err == nil {
				refs[abs] = true
			}
		}
	}
	return refs, nil
}

func depthOf(root, path string) int {
	rel, err := filepath.Rel(root, path)
	if err != nil || rel == "." {
		return 0
	}
	return strings.Count(rel, string(filepath.Separator)) + 1
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
