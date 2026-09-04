package catalog_test

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
	"github.com/casmith/ps2hdd/internal/logging"
)

func TestMain(m *testing.M) {
	logging.Discard()
	os.Exit(m.Run())
}

func writePS2ISO(t *testing.T, path, serial string) {
	t.Helper()
	data, err := isosynth.Build(isosynth.Image{
		VolumeID: serial,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS2SystemCNF(serial)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

// writePS1Disc lays down a MODE2/2352 BIN and its cuesheet.
func writePS1Disc(t *testing.T, dir, base, serial string) {
	t.Helper()
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: serial,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF(serial)},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	binName := base + ".bin"
	if err := os.WriteFile(filepath.Join(dir, binName), data, 0o600); err != nil {
		t.Fatal(err)
	}
	sheet := "FILE \"" + binName + "\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(dir, base+".cue"), []byte(sheet), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestScanPS2(t *testing.T) {
	root := t.TempDir()
	writePS2ISO(t, filepath.Join(root, "Burnout 3 Takedown.iso"), "SLUS_210.50")
	writePS2ISO(t, filepath.Join(root, "subdir", "God Hand.iso"), "SLUS_215.03")
	// Files that are not game images must be reported as problems, not
	// silently dropped and not treated as games.
	if err := os.WriteFile(filepath.Join(root, "notes.iso"), make([]byte, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "readme.txt"), []byte("hi"), 0o600); err != nil {
		t.Fatal(err)
	}

	s := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner())
	res, err := s.ScanPS2(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPS2: %v", err)
	}
	if len(res.Games) != 2 {
		t.Fatalf("got %d games, want 2: %+v", len(res.Games), res.Games)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0].Path, "notes.iso") {
		t.Errorf("problems = %+v, want just notes.iso", res.Problems)
	}
	titles := map[string]bool{}
	for _, g := range res.Games {
		titles[g.Title] = true
		if g.Platform != "ps2" {
			t.Errorf("%s: platform = %q", g.Title, g.Platform)
		}
		if g.Installed {
			t.Errorf("%s: a source scan must never mark a game installed", g.Title)
		}
		if g.SourcePath == "" {
			t.Errorf("%s: no source path", g.Title)
		}
	}
	if !titles["Burnout 3 Takedown"] || !titles["God Hand"] {
		t.Errorf("titles = %v", titles)
	}
}

func TestScanPS2UsesCache(t *testing.T) {
	root := t.TempDir()
	writePS2ISO(t, filepath.Join(root, "Game.iso"), "SLUS_200.02")
	c := catalog.NewMemoryCache()
	s := catalog.NewScanner(c, external.NewFakeRunner())

	first, err := s.ScanPS2(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached != 0 {
		t.Errorf("first scan served %d files from cache", first.Cached)
	}
	second, err := s.ScanPS2(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if second.Cached != 1 {
		t.Errorf("second scan served %d of %d files from cache", second.Cached, second.Scanned)
	}
	if len(second.Games) != 1 || second.Games[0].GameID != "SLUS_200.02" {
		t.Errorf("cached scan returned %+v", second.Games)
	}
}

func TestScanPS2CacheInvalidatedByChange(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "Game.iso")
	writePS2ISO(t, path, "SLUS_200.02")
	c := catalog.NewMemoryCache()
	s := catalog.NewScanner(c, external.NewFakeRunner())
	if _, err := s.ScanPS2(context.Background(), root); err != nil {
		t.Fatal(err)
	}

	// Replacing the image with a different game must not return the old
	// identity: the HDD and the source list would disagree about what a file
	// contains.
	writePS2ISO(t, path, "SLUS_210.50")
	res, err := s.ScanPS2(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if res.Cached != 0 {
		t.Error("a changed file was served from cache")
	}
	if len(res.Games) != 1 || res.Games[0].GameID != "SLUS_210.50" {
		t.Errorf("games = %+v", res.Games)
	}
}

func TestScanPS1GroupsDiscsAndIgnoresReferencedBins(t *testing.T) {
	root := t.TempDir()
	writePS1Disc(t, filepath.Join(root, "Final Fantasy VII"), "Disc 1", "SCUS_941.63")
	writePS1Disc(t, filepath.Join(root, "Final Fantasy VII"), "Disc 2", "SCUS_941.64")
	writePS1Disc(t, root, "Castlevania - Symphony of the Night", "SLUS_000.67")

	s := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner())
	res, err := s.ScanPS1(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPS1: %v", err)
	}
	if len(res.Games) != 2 {
		t.Fatalf("got %d titles, want 2: %+v", len(res.Games), res.Games)
	}
	byTitle := map[string]int{}
	for _, g := range res.Games {
		byTitle[g.Title] = g.DiscCount()
	}
	if byTitle["Final Fantasy VII"] != 2 {
		t.Errorf("Final Fantasy VII grouped as %d discs: %v", byTitle["Final Fantasy VII"], byTitle)
	}
	if byTitle["Castlevania - Symphony of the Night"] != 1 {
		t.Errorf("titles = %v", byTitle)
	}
	// A .bin named by a .cue must never appear as a title of its own.
	if len(res.Problems) != 0 {
		t.Errorf("problems = %+v; referenced BINs should not be inspected separately", res.Problems)
	}
}

func TestScanPS1BareBinIsStillFound(t *testing.T) {
	// A rip with no cuesheet has no CD-DA and is perfectly playable, so it
	// must not be skipped just because a sheet is missing.
	root := t.TempDir()
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "SLUS_000.67",
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF("SLUS_000.67")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Castlevania SOTN.bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	res, err := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner()).ScanPS1(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Games) != 1 || res.Games[0].GameID != "SLUS_000.67" {
		t.Fatalf("games = %+v", res.Games)
	}
}

func TestScanMissingDirectory(t *testing.T) {
	s := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner())
	if _, err := s.ScanPS2(context.Background(), filepath.Join(t.TempDir(), "nope")); err == nil {
		t.Error("scanning a missing directory should fail")
	}
	// An unset source directory is not an error: it simply yields nothing.
	res, err := s.ScanPS2(context.Background(), "")
	if err != nil || len(res.Games) != 0 {
		t.Errorf("empty root: %+v %v", res, err)
	}
}

func TestScanRespectsCancellation(t *testing.T) {
	root := t.TempDir()
	for i := 0; i < 5; i++ {
		writePS2ISO(t, filepath.Join(root, string(rune('a'+i))+".iso"), "SLUS_200.0"+string(rune('0'+i)))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, err := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner()).ScanPS2(ctx, root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Games) != 0 {
		t.Errorf("a cancelled scan returned %d games", len(res.Games))
	}
}

// The scanner must reject a cuesheet POPS cannot use rather than list it as
// installable and fail later.
// A split dump is installable now that Convert joins the tracks, so the scan
// has to list it rather than report it.
func TestScanPS1ListsSplitDumps(t *testing.T) {
	root := t.TempDir()
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "SPLIT",
		CDXA:     true,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF("SLUS_001.83")},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "a.bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	// An audio track: content does not matter, whole sectors do.
	if err := os.WriteFile(filepath.Join(root, "b.bin"), make([]byte, 2352*150), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "split.cue"),
		[]byte("FILE \"a.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\nFILE \"b.bin\" BINARY\n  TRACK 02 AUDIO\n    INDEX 00 00:00:00\n    INDEX 01 00:02:00\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	res, err := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner()).ScanPS1(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Problems) != 0 {
		t.Fatalf("problems = %+v", res.Problems)
	}
	if len(res.Games) != 1 || res.Games[0].GameID != "SLUS_001.83" {
		t.Fatalf("games = %+v", res.Games)
	}
}

// A cuesheet naming a file that is not there is still an error: joining cannot
// invent a missing track.
func TestScanPS1ReportsMissingTrack(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "split.cue"),
		[]byte("FILE \"a.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"),
		0o600); err != nil {
		t.Fatal(err)
	}
	res, err := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner()).ScanPS1(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Games) != 0 {
		t.Errorf("a cuesheet with no data file was listed: %+v", res.Games)
	}
	if len(res.Problems) != 1 || !strings.Contains(res.Problems[0].Reason, "missing") {
		t.Errorf("problems = %+v", res.Problems)
	}
}

// The scan cache has to survive between processes, or every launch of the
// interface rereads every image off a network share. This exercises the
// on-disk cache rather than the in-memory one.
func TestPersistentCacheSurvivesReopen(t *testing.T) {
	root := t.TempDir()
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	writePS2ISO(t, filepath.Join(root, "src", "Game.iso"), "SLUS_200.02")
	writePS2ISO(t, filepath.Join(root, "src", "Other.iso"), "SLUS_210.50")
	srcDir := filepath.Join(root, "src")

	c1, err := catalog.OpenCache("ps2-test")
	if err != nil {
		t.Fatalf("OpenCache: %v", err)
	}
	first, err := catalog.NewScanner(c1, external.NewFakeRunner()).ScanPS2(context.Background(), srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if first.Cached != 0 || len(first.Games) != 2 {
		t.Fatalf("first scan: %d games, %d cached", len(first.Games), first.Cached)
	}

	// A second Cache, as a second process would open.
	c2, err := catalog.OpenCache("ps2-test")
	if err != nil {
		t.Fatal(err)
	}
	if c2.Len() != 2 {
		t.Fatalf("the reopened cache holds %d entries, want 2", c2.Len())
	}
	second, err := catalog.NewScanner(c2, external.NewFakeRunner()).ScanPS2(context.Background(), srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if second.Cached != 2 {
		t.Errorf("second scan served %d of %d from cache", second.Cached, second.Scanned)
	}
	if len(second.Games) != 2 {
		t.Errorf("cached scan returned %d games", len(second.Games))
	}

	// A deleted file must not linger in the cache.
	if err := os.Remove(filepath.Join(srcDir, "Other.iso")); err != nil {
		t.Fatal(err)
	}
	third, err := catalog.NewScanner(c2, external.NewFakeRunner()).ScanPS2(context.Background(), srcDir)
	if err != nil {
		t.Fatal(err)
	}
	if len(third.Games) != 1 {
		t.Errorf("after deleting a file the scan returned %d games", len(third.Games))
	}
	if c2.Len() != 1 {
		t.Errorf("the cache still holds %d entries after a prune", c2.Len())
	}

	if err := c2.Clear(); err != nil {
		t.Fatal(err)
	}
	c3, err := catalog.OpenCache("ps2-test")
	if err != nil {
		t.Fatal(err)
	}
	if c3.Len() != 0 {
		t.Errorf("Clear left %d entries", c3.Len())
	}
}

// A scan of a large library is the slowest thing ps2hdd does, and the only
// thing a caller can show while it runs is what the scanner reports. These
// assert the three properties any progress display depends on: the count
// reaches the total, it advances one file at a time, and the reported position
// never goes backwards even though files are inspected concurrently.
func TestScanReportsProgress(t *testing.T) {
	root := t.TempDir()
	const n = 300
	for i := 0; i < n; i++ {
		// Names sort in the same order as the index, so a report that goes
		// backwards alphabetically is a report that went backwards.
		name := fmt.Sprintf("game-%03d.iso", i)
		writePS2ISO(t, filepath.Join(root, name), fmt.Sprintf("SLUS_%03d.%02d", i, i%100))
	}

	s := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner())
	// Several files in flight at once is the case the ordering guarantee
	// exists for; with one worker every implementation looks correct.
	s.Concurrency = 8
	var reports []catalog.ScanProgress
	s.OnProgress = func(p catalog.ScanProgress) { reports = append(reports, p) }

	if _, err := s.ScanPS2(context.Background(), root); err != nil {
		t.Fatalf("ScanPS2: %v", err)
	}

	if len(reports) != n {
		t.Fatalf("got %d progress reports, want one per file (%d)", len(reports), n)
	}
	for i, p := range reports {
		if p.Done != i+1 {
			t.Fatalf("report %d: Done = %d, want %d; the counter must not skip or repeat", i, p.Done, i+1)
		}
		if p.Total != n {
			t.Errorf("report %d: Total = %d, want %d", i, p.Total, n)
		}
		if p.Root != root {
			t.Errorf("report %d: Root = %q, want %q", i, p.Root, root)
		}
		if i > 0 && p.Path < reports[i-1].Path {
			t.Fatalf("report %d went backwards: %q after %q", i, filepath.Base(p.Path), filepath.Base(reports[i-1].Path))
		}
	}
	if got, want := filepath.Base(reports[n-1].Path), fmt.Sprintf("game-%03d.iso", n-1); got != want {
		t.Errorf("the last report names %q, want the last file %q", got, want)
	}
	if reports[0].Cached {
		t.Error("the first scan of a file reported it as cached")
	}
}

// A rescan is fast because it reads the cache rather than the images. Progress
// has to say so, or the speed looks like files being skipped.
func TestScanProgressReportsCacheHits(t *testing.T) {
	root := t.TempDir()
	writePS2ISO(t, filepath.Join(root, "Game.iso"), "SLUS_200.02")
	c := catalog.NewMemoryCache()
	s := catalog.NewScanner(c, external.NewFakeRunner())

	if _, err := s.ScanPS2(context.Background(), root); err != nil {
		t.Fatalf("first scan: %v", err)
	}
	var reports []catalog.ScanProgress
	s.OnProgress = func(p catalog.ScanProgress) { reports = append(reports, p) }
	if _, err := s.ScanPS2(context.Background(), root); err != nil {
		t.Fatalf("rescan: %v", err)
	}
	if len(reports) != 1 {
		t.Fatalf("got %d reports, want 1", len(reports))
	}
	if !reports[0].Cached {
		t.Error("a rescan of an unchanged file did not report a cache hit")
	}
}

// A scanner with no progress hook is the normal case and must not panic.
func TestScanWithoutProgressHook(t *testing.T) {
	root := t.TempDir()
	writePS2ISO(t, filepath.Join(root, "Game.iso"), "SLUS_200.02")
	s := catalog.NewScanner(catalog.NewMemoryCache(), external.NewFakeRunner())
	if _, err := s.ScanPS2(context.Background(), root); err != nil {
		t.Fatalf("ScanPS2: %v", err)
	}
}
