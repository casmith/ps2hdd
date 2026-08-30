package ps2_test

import (
	"bytes"
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
	return writeISOMedia(t, name, serial, padBlocks, false)
}

// writeISOMedia writes an image, optionally carrying the CD-ROM XA signature
// that marks it as a CD.
func writeISOMedia(t *testing.T, name, serial string, padBlocks uint32, cdxa bool) string {
	t.Helper()
	data, err := isosynth.Build(isosynth.Image{
		VolumeID:  serial,
		PadBlocks: padBlocks,
		CDXA:      cdxa,
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
	path := writeISOMedia(t, "Ridge Racer V.iso", "SLUS_200.02", 0, true)
	img, err := ps2.Inspect(path)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if img.GameID != "SLUS_200.02" {
		t.Errorf("GameID = %q", img.GameID)
	}
	if img.Media != model.MediaCD {
		t.Errorf("Media = %q, want cd for a CD-XA image", img.Media)
	}
	if img.MediaFromSize {
		t.Error("the media type was guessed from the size despite an XA signature")
	}
	if img.Title != "Ridge Racer V" {
		t.Errorf("Title = %q", img.Title)
	}
	if img.BootFile != `cdrom0:\SLUS_200.02;1` {
		t.Errorf("BootFile = %q", img.BootFile)
	}
}

func TestInspectDVD(t *testing.T) {
	// No XA signature: a blank XA area is what a DVD image looks like.
	path := writeISO(t, "Burnout 3 Takedown.iso", "SLUS_210.50", 0)
	img, err := ps2.Inspect(path)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if img.Media != model.MediaDVD {
		t.Errorf("Media = %q, want dvd for an image with a blank XA area", img.Media)
	}
	if img.MediaFromSize {
		t.Error("the media type was guessed despite a conclusive blank XA area")
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

// The XA signature decides the media type, not the image size. A small DVD
// image and a large CD image are both real -- a mostly-empty DVD rip, or a CD
// image padded by a ripper -- and getting this wrong installs a game with the
// wrong hdl_dump verb, which will not boot.
func TestMediaTypeIgnoresSizeWhenTheSignatureIsConclusive(t *testing.T) {
	// A tiny image with no XA signature is still a DVD.
	small := writeISO(t, "small.iso", "SLUS_200.03", 0)
	img, err := ps2.Inspect(small)
	if err != nil {
		t.Fatal(err)
	}
	if img.Media != model.MediaDVD {
		t.Errorf("small blank-XA image = %q, want dvd", img.Media)
	}

	// A large image that carries the XA signature is still a CD.
	pad := uint32(800 * 1024 * 1024 / iso9660.LogicalSectorSize)
	big := writeISOMedia(t, "big.iso", "SLUS_200.04", pad, true)
	img, err = ps2.Inspect(big)
	if err != nil {
		t.Fatal(err)
	}
	if img.Media != model.MediaCD {
		t.Errorf("large CD-XA image = %q, want cd", img.Media)
	}
	if img.MediaFromSize {
		t.Error("MediaFromSize set despite a conclusive signature")
	}
}

// When the XA area holds neither the signature nor zeroes there is nothing
// authoritative to go on, and the size heuristic takes over -- flagged as a
// guess so callers can say so.
func TestMediaTypeFallsBackToSize(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name string
		pad  uint32
		want model.MediaType
	}{
		{"junk-small.iso", 0, model.MediaCD},
		{"junk-big.iso", uint32(800 * 1024 * 1024 / iso9660.LogicalSectorSize), model.MediaDVD},
	} {
		data, err := isosynth.Build(isosynth.Image{
			VolumeID:  "SLUS_200.05",
			PadBlocks: tc.pad,
			Files:     map[string][]byte{"SYSTEM.CNF": isosynth.PS2SystemCNF("SLUS_200.05")},
		})
		if err != nil {
			t.Fatal(err)
		}
		// Scribble something inconclusive over the XA area of the descriptor.
		copy(data[16*iso9660.LogicalSectorSize+1024:], "MASTERED")
		path := filepath.Join(dir, tc.name)
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		img, err := ps2.Inspect(path)
		if err != nil {
			t.Fatal(err)
		}
		if img.Media != tc.want {
			t.Errorf("%s: media = %q, want %q", tc.name, img.Media, tc.want)
		}
		if !img.MediaFromSize {
			t.Errorf("%s: MediaFromSize not set for an inconclusive signature", tc.name)
		}
	}
}

// A CD-based PS2 title ripped raw is MODE2/2352, not a 2048 stream. Only 2048
// used to be tried, which made every such rip unidentifiable.
func TestInspectReadsRaw2352Images(t *testing.T) {
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "RAW_CD_GAME",
		CDXA:     true,
		Files: map[string][]byte{
			"SYSTEM.CNF":  isosynth.PS2SystemCNF("SLUS_200.35"),
			"SLUS_200.35": []byte("ELF"),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "Raw Game (USA).bin")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := ps2.Inspect(path)
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if img.GameID != "SLUS_200.35" {
		t.Errorf("GameID = %q", img.GameID)
	}
	if img.Media != model.MediaCD {
		t.Errorf("Media = %q, want cd for a raw 2352 rip", img.Media)
	}
}

// On a real library SYSTEM.CNF frequently sits gigabytes into the image, out
// of reach of a partial read. The root directory always holds the boot ELF,
// named for the serial, and that is the fallback.
func TestInspectAtFallsBackToTheRootDirectory(t *testing.T) {
	data, err := isosynth.Build(isosynth.Image{
		VolumeID: "LATE_CNF",
		Files: map[string][]byte{
			"SYSTEM.CNF":  isosynth.PS2SystemCNF("SLUS_212.81"),
			"SLUS_212.81": []byte("ELF"),
		},
		PadBlocks: 400,
	})
	if err != nil {
		t.Fatal(err)
	}
	// Cut the image at the start of the file area: the volume descriptor and
	// the root directory are present, no file content is. That is the shape
	// of a bounded read whose SYSTEM.CNF lies further in.
	head := data[:21*2048]

	if _, err := ps2.InspectAt(bytes.NewReader(head), int64(len(data)), "Late (USA).iso", false); err == nil {
		t.Error("a complete read was allowed to guess the serial from the directory")
	}

	img, err := ps2.InspectAt(bytes.NewReader(head), int64(len(data)), "Late (USA).iso", true)
	if err != nil {
		t.Fatalf("partial InspectAt: %v", err)
	}
	if img.GameID != "SLUS_212.81" {
		t.Errorf("GameID = %q", img.GameID)
	}
	// The size must come from the volume descriptor, not from the fragment.
	if img.SizeBytes != int64(len(data)) {
		t.Errorf("SizeBytes = %d, want %d", img.SizeBytes, len(data))
	}
}
