package ps2_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/iso9660"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps2"
)

func writeISO(t *testing.T, name, serial string, padBlocks uint32) string {
	t.Helper()
	data, err := isosynth.Build(isosynth.Image{
		VolumeID:  serial,
		PadBlocks: padBlocks,
		Files: map[string][]byte{
			"SYSTEM.CNF": isosynth.PS2SystemCNF(serial),
			serial:       []byte("boot elf"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectCD(t *testing.T) {
	path := writeISO(t, "Ridge Racer V.iso", "SLUS_200.02", 0)
	img, err := ps2.Inspect(path)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if img.GameID != "SLUS_200.02" {
		t.Errorf("GameID = %q", img.GameID)
	}
	if img.Media != model.MediaCD {
		t.Errorf("Media = %q, want cd for a small image", img.Media)
	}
	if img.Title != "Ridge Racer V" {
		t.Errorf("Title = %q", img.Title)
	}
	if img.BootFile != `cdrom0:\SLUS_200.02;1` {
		t.Errorf("BootFile = %q", img.BootFile)
	}
}

func TestInspectDVD(t *testing.T) {
	// Pad past the CD size limit so the image must be classified as a DVD.
	pad := uint32(800 * 1024 * 1024 / iso9660.LogicalSectorSize)
	path := writeISO(t, "Burnout 3 Takedown.iso", "SLUS_210.50", pad)
	img, err := ps2.Inspect(path)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if img.Media != model.MediaDVD {
		t.Errorf("Media = %q, want dvd for an 800 MB image", img.Media)
	}
	g := img.Game()
	if g.Platform != model.PlatformPS2 || g.DiscCount() != 1 {
		t.Errorf("Game() = %+v", g)
	}
	if g.Key() != "ps2:SLUS21050" {
		t.Errorf("Key = %q", g.Key())
	}
}

func TestInspectRejectsNonISO(t *testing.T) {
	path := filepath.Join(t.TempDir(), "notaniso.iso")
	if err := os.WriteFile(path, make([]byte, 128*1024), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ps2.Inspect(path)
	if !errors.Is(err, ps2.ErrNotPS2) {
		t.Fatalf("err = %v, want ErrNotPS2", err)
	}
}

func TestInspectRejectsISOWithoutSystemCNF(t *testing.T) {
	data, err := isosynth.Build(isosynth.Image{
		VolumeID: "SOMETHING",
		Files:    map[string][]byte{"README.TXT": []byte("hi")},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "data.iso")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ps2.Inspect(path); !errors.Is(err, ps2.ErrNotPS2) {
		t.Fatalf("err = %v, want ErrNotPS2", err)
	}
}

func TestParseSystemCNF(t *testing.T) {
	const cnf = "BOOT2 = cdrom0:\\SLUS_209.46;1\r\nVER = 1.00\r\nVMODE = NTSC\r\nHDDUNITPOWER = NICHDD\r\n"
	for _, c := range []struct{ key, want string }{
		{"BOOT2", `cdrom0:\SLUS_209.46;1`},
		{"boot2", `cdrom0:\SLUS_209.46;1`}, // real discs vary in case
		{"VMODE", "NTSC"},
	} {
		got, ok := ps2.ParseSystemCNF(cnf, c.key)
		if !ok || got != c.want {
			t.Errorf("ParseSystemCNF(%q) = %q,%v want %q", c.key, got, ok, c.want)
		}
	}
	if _, ok := ps2.ParseSystemCNF(cnf, "BOOT"); ok {
		t.Error("a PS1 BOOT key was found in a PS2 SYSTEM.CNF")
	}
}

func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"SLUS_210.50.Burnout 3 Takedown": "Burnout 3 Takedown",
		"SLUS-21050 - Burnout 3":         "Burnout 3",
		"God_Hand":                       "God Hand",
		"Shadow of the Colossus":         "Shadow of the Colossus",
		"":                               "",
	}
	for in, want := range cases {
		if got := ps2.CleanTitle(in); got != want {
			t.Errorf("CleanTitle(%q) = %q, want %q", in, got, want)
		}
	}
}
