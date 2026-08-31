package catalog_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
	"github.com/casmith/ps2hdd/internal/model"
)

// sevenZip skips a test when no archive tool is installed, so the suite still
// runs on a machine without p7zip.
func sevenZip(t *testing.T) string {
	t.Helper()
	for _, name := range []string{external.SevenZipTool, external.SevenZipAltTool} {
		if p, err := exec.LookPath(name); err == nil {
			return p
		}
	}
	t.Skipf("neither %s nor %s is installed", external.SevenZipTool, external.SevenZipAltTool)
	return ""
}

// buildArchivedISO writes a synthetic PS2 image and packs it into a .7z.
func buildArchivedISO(t *testing.T, dir, name, serial string, opts isosynth.Image) string {
	t.Helper()
	sevenZip(t)

	opts.Files = map[string][]byte{"SYSTEM.CNF": isosynth.PS2SystemCNF(serial)}
	// The boot ELF at the root is what identification falls back to when
	// SYSTEM.CNF is out of reach, so the synthetic disc carries one too.
	opts.Files[serial] = []byte("ELF")
	data, err := isosynth.Build(opts)
	if err != nil {
		t.Fatal(err)
	}
	iso := filepath.Join(t.TempDir(), name+".iso")
	if err := os.WriteFile(iso, data, 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(dir, name+".7z")
	// -mx=0 stores rather than compresses: these tests are about the plumbing,
	// not about LZMA.
	cmd := exec.Command(sevenZip(t), "a", "-mx=0", "-y", archive, iso)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", archive, err, out)
	}
	return archive
}

func TestScanPS2FindsGamesInsideArchives(t *testing.T) {
	root := t.TempDir()
	buildArchivedISO(t, root, "Some Game (USA)", "SLUS_202.16", isosynth.Image{
		VolumeID: "SOME_GAME", PadBlocks: 400,
	})

	s := catalog.NewScanner(catalog.NewMemoryCache(), &external.ExecRunner{})
	res, err := s.ScanPS2(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPS2: %v", err)
	}
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if len(res.Games) != 1 {
		t.Fatalf("got %d games, want 1: %+v", len(res.Games), res.Games)
	}
	g := res.Games[0]
	if g.GameID != "SLUS_202.16" {
		t.Errorf("GameID = %q", g.GameID)
	}
	// The archive is the source, and the member has to be recorded or the
	// install path cannot know it needs to extract anything.
	if !strings.HasSuffix(g.SourcePath, ".7z") {
		t.Errorf("SourcePath = %q, want the archive", g.SourcePath)
	}
	if g.ArchiveMember == "" {
		t.Error("ArchiveMember is empty, so an install would hand hdl_dump the archive")
	}
	for _, d := range g.Discs {
		if d.ArchiveMember != g.ArchiveMember || d.SourcePath != g.SourcePath {
			t.Errorf("disc does not carry the archive location: %+v", d)
		}
	}
	// Size comes from the image inside, never from the compressed container.
	if g.SizeBytes <= 0 {
		t.Errorf("SizeBytes = %d", g.SizeBytes)
	}
}

// An archive that holds no image is reported, not silently dropped: a user who
// sees a title missing from the list needs to know why.
func TestScanPS2ReportsArchivesWithNoImage(t *testing.T) {
	sevenZip(t)
	root := t.TempDir()

	junk := filepath.Join(t.TempDir(), "readme.txt")
	if err := os.WriteFile(junk, []byte("not a game"), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "Tools.7z")
	if out, err := exec.Command(sevenZip(t), "a", "-mx=0", "-y", archive, junk).CombinedOutput(); err != nil {
		t.Fatalf("build archive: %v\n%s", err, out)
	}

	s := catalog.NewScanner(catalog.NewMemoryCache(), &external.ExecRunner{})
	res, err := s.ScanPS2(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPS2: %v", err)
	}
	if len(res.Games) != 0 {
		t.Errorf("got %d games from an archive with no image", len(res.Games))
	}
	if len(res.Problems) != 1 {
		t.Fatalf("got %d problems, want 1: %+v", len(res.Problems), res.Problems)
	}
	if !strings.Contains(res.Problems[0].Reason, "no disc image") {
		t.Errorf("reason = %q", res.Problems[0].Reason)
	}
}

func TestFindImage(t *testing.T) {
	one := []external.ArchiveEntry{
		{Name: "readme.txt"},
		{Name: "Game (USA).iso", SizeBytes: 4700000000},
	}
	got, err := catalog.FindImage(one)
	if err != nil || got.Name != "Game (USA).iso" {
		t.Errorf("FindImage = %+v, %v", got, err)
	}

	if _, err := catalog.FindImage([]external.ArchiveEntry{{Name: "readme.txt"}}); err == nil {
		t.Error("an archive with no image was accepted")
	}
	// Two images is a packaging decision, not something to pick between.
	_, err = catalog.FindImage([]external.ArchiveEntry{
		{Name: "Disc 1.iso"}, {Name: "Disc 2.iso"},
	})
	if err == nil {
		t.Error("an archive with two images was accepted")
	}
}

func TestFindPS1Members(t *testing.T) {
	cases := map[string]struct {
		entries   []external.ArchiveEntry
		wantCue   string
		wantData  string
		wantCount int
		wantErr   string
	}{
		"cue and one bin": {
			entries: []external.ArchiveEntry{
				{Name: "Zero Divide (USA).bin", SizeBytes: 411026112},
				{Name: "Zero Divide (USA).cue", SizeBytes: 83},
			},
			wantCue: "Zero Divide (USA).cue", wantData: "Zero Divide (USA).bin", wantCount: 1,
		},
		// A multi-track rip: the data track is track 1, not whichever the
		// archive listed first.
		"multi-track picks track 1": {
			entries: []external.ArchiveEntry{
				{Name: "Zoop (USA) (Track 3).bin"},
				{Name: "Zoop (USA) (Track 1).bin"},
				{Name: "Zoop (USA) (Track 2).bin"},
				{Name: "Zoop (USA).cue"},
			},
			wantCue: "Zoop (USA).cue", wantData: "Zoop (USA) (Track 1).bin", wantCount: 3,
		},
		"iso with no cue": {
			entries:  []external.ArchiveEntry{{Name: "Game.iso"}},
			wantCue:  "",
			wantData: "Game.iso", wantCount: 1,
		},
		// The multi-part RAR sets some collections use: an archive of
		// archives. Saying so beats "no disc image", which is true but sends
		// the reader looking for the wrong thing.
		"nested archive": {
			entries: []external.ArchiveEntry{
				{Name: "[SLUS_013.00] 007 Racing.part01.rar"},
				{Name: "[SLUS_013.00] 007 Racing.part02.rar"},
			},
			wantErr: "nested archive",
		},
		"nothing usable": {
			entries: []external.ArchiveEntry{{Name: "readme.txt"}},
			wantErr: "no disc image",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m, err := catalog.FindPS1Members(tc.entries)
			if tc.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
					t.Fatalf("err = %v, want one mentioning %q", err, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("FindPS1Members: %v", err)
			}
			if m.Cue != tc.wantCue {
				t.Errorf("cue = %q, want %q", m.Cue, tc.wantCue)
			}
			if m.Data.Name != tc.wantData {
				t.Errorf("data = %q, want %q", m.Data.Name, tc.wantData)
			}
			if m.DataCount != tc.wantCount {
				t.Errorf("count = %d, want %d", m.DataCount, tc.wantCount)
			}
		})
	}
}

// A PS1 library is very often entirely archived, so the scan has to look
// inside them or find almost nothing.
func TestScanPS1FindsGamesInsideArchives(t *testing.T) {
	sevenZip(t)
	root := t.TempDir()
	work := t.TempDir()

	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "ZERODIVIDE",
		CDXA:     true,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF("SLUS_001.83")},
	})
	if err != nil {
		t.Fatal(err)
	}
	const stem = "Zero Divide (USA)"
	if err := os.WriteFile(filepath.Join(work, stem+".bin"), data, 0o600); err != nil {
		t.Fatal(err)
	}
	cue := "FILE \"" + stem + ".bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"
	if err := os.WriteFile(filepath.Join(work, stem+".cue"), []byte(cue), 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, stem+".zip")
	cmd := exec.Command(sevenZip(t), "a", "-mx=0", "-y", archive, ".")
	cmd.Dir = work
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build archive: %v\n%s", err, out)
	}

	s := catalog.NewScanner(catalog.NewMemoryCache(), &external.ExecRunner{})
	res, err := s.ScanPS1(context.Background(), root)
	if err != nil {
		t.Fatalf("ScanPS1: %v", err)
	}
	if len(res.Problems) > 0 {
		t.Fatalf("problems: %+v", res.Problems)
	}
	if len(res.Games) != 1 {
		t.Fatalf("got %d games, want 1: %+v", len(res.Games), res.Games)
	}
	g := res.Games[0]
	if g.GameID != "SLUS_001.83" {
		t.Errorf("GameID = %q", g.GameID)
	}
	if g.Platform != model.PlatformPS1 {
		t.Errorf("platform = %q", g.Platform)
	}
	// The cuesheet is the member handed on, because that is what the
	// converter reads.
	if filepath.Ext(g.ArchiveMember) != ".cue" {
		t.Errorf("ArchiveMember = %q, want the cuesheet", g.ArchiveMember)
	}
	// The size is the data track's, not the compressed container's.
	if g.SizeBytes != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", g.SizeBytes, len(data))
	}
}

// `install game.7z` and picking the same file out of the library must reach
// the same code and produce the same entry. The title is the part that used to
// give it away: an archive whose member is named after the serial alone leaves
// nothing once the serial is stripped, and the game reached the console called
// "(1 00)".
func TestArchivedTitleFallsBackToTheArchiveName(t *testing.T) {
	root := t.TempDir()
	// Build the image, then repack it under a member name that carries no
	// title of its own -- the shape a good many real archives actually use.
	iso := filepath.Join(t.TempDir(), "SLUS-20152 (1.00).iso")
	data, err := isosynth.Build(isosynth.Image{
		VolumeID: "ACE_COMBAT_04", PadBlocks: 400,
		Files: map[string][]byte{
			"SYSTEM.CNF":  isosynth.PS2SystemCNF("SLUS_201.52"),
			"SLUS_201.52": []byte("ELF"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(iso, data, 0o600); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(root, "Ace Combat 04 - Shattered Skies (USA).7z")
	cmd := exec.Command(sevenZip(t), "a", "-mx=0", "-y", archive, iso)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build %s: %v\n%s", archive, err, out)
	}

	a := external.Archive{Runner: &external.ExecRunner{}}
	g, _, err := catalog.InspectArchivedPS2(context.Background(), a, archive)
	if err != nil {
		t.Fatal(err)
	}
	if g.GameID != "SLUS_201.52" {
		t.Errorf("GameID = %q", g.GameID)
	}
	if g.Title != "Ace Combat 04 - Shattered Skies (USA)" {
		t.Errorf("Title = %q, want the archive's own name", g.Title)
	}
}
