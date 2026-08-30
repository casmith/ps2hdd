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
