package apa_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/apa/apasynth"
)

func buildDisk(t *testing.T, d apasynth.Disk) (*os.File, int64) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "ps2hdd.img")
	if err := apasynth.Write(path, d); err != nil {
		t.Fatalf("build synthetic disk: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { f.Close() })
	fi, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	return f, fi.Size()
}

func TestIsAPA(t *testing.T) {
	f, _ := buildDisk(t, apasynth.DefaultDisk())
	ok, err := apa.IsAPA(f)
	if err != nil {
		t.Fatal(err)
	}
	if !ok {
		t.Fatal("synthetic APA disk was not recognised as APA")
	}
}

func TestIsAPARejectsForeignDisk(t *testing.T) {
	path := filepath.Join(t.TempDir(), "not-apa.img")
	// A disk full of zeroes has no magic; a disk with the magic but a wrong
	// checksum must be rejected just as firmly, since that is the case where a
	// half-written or foreign table could be mistaken for a PS2 HDD.
	if err := os.WriteFile(path, make([]byte, 1<<20), 0o600); err != nil {
		t.Fatal(err)
	}
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if ok, err := apa.IsAPA(f); err != nil || ok {
		t.Fatalf("zeroed disk: got ok=%v err=%v, want false/nil", ok, err)
	}

	bad := make([]byte, 1<<20)
	copy(bad[4:8], []byte{'A', 'P', 'A', 0})
	if err := os.WriteFile(path, bad, 0o600); err != nil {
		t.Fatal(err)
	}
	f2, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f2.Close()
	if ok, _ := apa.IsAPA(f2); ok {
		t.Fatal("disk with APA magic but a bad checksum was accepted")
	}
	if _, err := apa.ReadTOC(f2, 1<<20); err == nil {
		t.Fatal("ReadTOC accepted a table with a bad master boot record checksum")
	}
}

func TestReadTOC(t *testing.T) {
	f, size := buildDisk(t, apasynth.DefaultDisk())
	toc, err := apa.ReadTOC(f, size)
	if err != nil {
		t.Fatalf("ReadTOC: %v", err)
	}
	if len(toc.Slices) != 1 {
		t.Fatalf("slices = %d, want 1", len(toc.Slices))
	}
	want := []string{"__mbr", "__net", "__system", "__sysconf", "__common", "+OPL", "__.POPS"}
	parts := toc.Partitions()
	for _, id := range want {
		if _, _, ok := toc.Find(id); !ok {
			t.Errorf("partition %q missing from table of %d partitions", id, len(parts))
		}
	}
	if _, _, ok := toc.Find("+opl"); !ok {
		t.Error("Find is not case-insensitive")
	}
	if _, _, ok := toc.Find("__.MISSING"); ok {
		t.Error("Find reported a partition that does not exist")
	}

	total, used, free := toc.Chunks()
	if total == 0 || used == 0 || free == 0 || used+free != total {
		t.Errorf("chunk accounting inconsistent: total=%d used=%d free=%d", total, used, free)
	}
}

func TestReadGames(t *testing.T) {
	d := apasynth.DefaultDisk()
	f, size := buildDisk(t, d)
	toc, err := apa.ReadTOC(f, size)
	if err != nil {
		t.Fatal(err)
	}
	games, err := apa.ReadGames(f, toc)
	if err != nil {
		t.Fatalf("ReadGames: %v", err)
	}
	if len(games) != len(d.Games) {
		t.Fatalf("got %d games, want %d", len(games), len(d.Games))
	}
	byStartup := map[string]apa.GameInfo{}
	for _, g := range games {
		byStartup[g.Startup] = g
	}
	for _, want := range d.Games {
		got, ok := byStartup[want.Startup]
		if !ok {
			t.Errorf("game %s missing", want.Startup)
			continue
		}
		if got.Name != want.Name {
			t.Errorf("%s: name = %q, want %q", want.Startup, got.Name, want.Name)
		}
		if got.IsDVD != want.IsDVD {
			t.Errorf("%s: IsDVD = %v, want %v", want.Startup, got.IsDVD, want.IsDVD)
		}
		if got.DMAMode() != "*u4" {
			t.Errorf("%s: DMAMode = %q, want *u4", want.Startup, got.DMAMode())
		}
		if got.CompatFlagList() != "0" {
			t.Errorf("%s: CompatFlagList = %q, want 0", want.Startup, got.CompatFlagList())
		}
		// The image is a whole number of megabytes, so the recorded raw size
		// must match exactly; the allocated size rounds up to 128 MiB chunks
		// and so is at least as large.
		if wantBytes := int64(want.SizeMB) * 1024 * 1024; got.ImageBytes() != wantBytes {
			t.Errorf("%s: image bytes = %d, want %d", want.Startup, got.ImageBytes(), wantBytes)
		}
		if got.SizeBytes() < got.ImageBytes() {
			t.Errorf("%s: allocated %d < image %d", want.Startup, got.SizeBytes(), got.ImageBytes())
		}
	}
}

func TestPartitionNameTruncates(t *testing.T) {
	long := "A Very Long Game Title That Will Not Fit In The Id Field"
	name := apa.PartitionName("SLUS_209.46", long, false)
	if len(name) != 32 {
		t.Errorf("len = %d, want 32 (%q)", len(name), name)
	}
	if got, want := name[:15], "PP.SLUS_209.46."; got != want {
		t.Errorf("prefix = %q, want %q", got, want)
	}
	hidden := apa.PartitionName("SLUS_209.46", "X", true)
	if got, want := hidden, "__.SLUS_209.46.X"; got != want {
		t.Errorf("hidden = %q, want %q", got, want)
	}
}

func TestCompatFlagRendering(t *testing.T) {
	g := apa.GameInfo{CompatFlags: 0b00000101}
	if got := g.CompatFlagList(); got != "+1+3" {
		t.Errorf("CompatFlagList = %q, want +1+3", got)
	}
}
