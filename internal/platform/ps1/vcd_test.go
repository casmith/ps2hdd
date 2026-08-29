package ps1_test

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// The reference fixtures were produced by cue2pops v2.0. See
// testdata/vcd/README.md for exactly how.
var headerCases = []struct {
	name     string
	cue      string
	fixture  string
	binBytes int64
}{
	{"multitrack", "multitrack.cue", "multitrack.header.bin", 36000 * ps1.SectorSize},
	{"single", "single.cue", "single.header.bin", 9000 * ps1.SectorSize},
	{"cdrwin", "cdrwin.cue", "cdrwin.header.bin", 36000 * ps1.SectorSize},
}

func TestBuildHeaderMatchesCue2POPS(t *testing.T) {
	for _, c := range headerCases {
		t.Run(c.name, func(t *testing.T) {
			cue, err := ps1.ParseCueFile(filepath.Join("../../../testdata/cue", c.cue))
			if err != nil {
				t.Fatalf("ParseCueFile: %v", err)
			}
			// The fixture cuesheets reference BINs that are not in the
			// repository, so validation of the referenced file is bypassed by
			// clearing the resolved path; everything else is checked.
			cue.BinPath = ""

			got, err := ps1.BuildHeader(cue, c.binBytes)
			if err != nil {
				t.Fatalf("BuildHeader: %v", err)
			}
			if len(got) != ps1.HeaderSize {
				t.Fatalf("header is %d bytes, want %d", len(got), ps1.HeaderSize)
			}
			want, err := os.ReadFile(filepath.Join("../../../testdata/vcd", c.fixture))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got[:len(want)], want) {
				t.Errorf("header differs from cue2pops output:\n got %s\nwant %s",
					hexDump(got[:len(want)]), hexDump(want))
			}
			// The reference header is zero past the fixture, so ours must be too.
			for i, b := range got[len(want):] {
				if b != 0 {
					t.Fatalf("byte %d past the fixture is %#02x, want 0", len(want)+i, b)
				}
			}
		})
	}
}

func hexDump(b []byte) string {
	var sb bytes.Buffer
	for i := 0; i < len(b); i += 16 {
		end := i + 16
		if end > len(b) {
			end = len(b)
		}
		chunk := b[i:end]
		allZero := true
		for _, c := range chunk {
			if c != 0 {
				allZero = false
			}
		}
		if allZero {
			continue
		}
		sb.WriteString("\n  ")
		for _, c := range chunk {
			sb.WriteString(hexByte(c))
			sb.WriteByte(' ')
		}
	}
	return sb.String()
}

func hexByte(b byte) string {
	const d = "0123456789abcdef"
	return string([]byte{d[b>>4], d[b&0xf]})
}

func TestBuildHeaderRejectsTruncatedBIN(t *testing.T) {
	cue, err := ps1.ParseCueFile("../../../testdata/cue/single.cue")
	if err != nil {
		t.Fatal(err)
	}
	cue.BinPath = ""
	if _, err := ps1.BuildHeader(cue, 9000*ps1.SectorSize+7); err == nil {
		t.Fatal("BuildHeader accepted a BIN that is not a whole number of sectors")
	}
	if _, err := ps1.BuildHeader(cue, 0); err == nil {
		t.Fatal("BuildHeader accepted an empty BIN")
	}
}

func TestPadOffset(t *testing.T) {
	cdrwin, err := ps1.ParseCueFile("../../../testdata/cue/cdrwin.cue")
	if err != nil {
		t.Fatal(err)
	}
	cdrwin.BinPath = ""
	// The first audio track starts at 03:20:00, which is LBA 15000.
	off, need := ps1.PadOffset(cdrwin, 36000*ps1.SectorSize)
	if !need {
		t.Fatal("a CDRWIN-style sheet needs a materialised pregap")
	}
	if want := int64(15000 * ps1.SectorSize); off != want {
		t.Errorf("pad offset = %d, want %d", off, want)
	}

	// A sheet with no PREGAP command needs no padding.
	multi, err := ps1.ParseCueFile("../../../testdata/cue/multitrack.cue")
	if err != nil {
		t.Fatal(err)
	}
	multi.BinPath = ""
	if _, need := ps1.PadOffset(multi, 1); need {
		t.Error("a non-CDRWIN sheet should need no padding")
	}
}

// TestConvertMatchesCue2POPS runs the whole conversion, including the disc
// copy and the CDRWIN pregap insertion, and compares the result to what
// cue2pops produces for the same input.
func TestConvertMatchesCue2POPS(t *testing.T) {
	dir := t.TempDir()
	// A small disc: 900 sectors of data, then audio at 00:08:00 (LBA 600).
	const sectors = 900
	bin := make([]byte, sectors*ps1.SectorSize)
	for i := range bin {
		bin[i] = byte(i % 251)
	}
	if err := os.WriteFile(filepath.Join(dir, "g.bin"), bin, 0o600); err != nil {
		t.Fatal(err)
	}
	cuePath := filepath.Join(dir, "g.cue")
	const sheet = `FILE "g.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PREGAP 00:02:00
    INDEX 01 00:08:00
`
	if err := os.WriteFile(cuePath, []byte(sheet), 0o600); err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(dir, "g.vcd")
	var lastFrac float64
	if err := ps1.Convert(cuePath, out, ps1.ConvertOptions{
		OnProgress: func(f float64) { lastFrac = f },
	}); err != nil {
		t.Fatalf("Convert: %v", err)
	}
	if lastFrac != 1 {
		t.Errorf("final progress = %v, want 1", lastFrac)
	}

	got, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	// Header + disc + one 150-sector pregap.
	wantSize := ps1.HeaderSize + len(bin) + 150*ps1.SectorSize
	if len(got) != wantSize {
		t.Fatalf("VCD is %d bytes, want %d", len(got), wantSize)
	}
	// The pregap goes immediately before the audio track at LBA 600.
	const padAt = 600 * ps1.SectorSize
	if !bytes.Equal(got[ps1.HeaderSize:ps1.HeaderSize+padAt], bin[:padAt]) {
		t.Error("data before the pregap does not match the source")
	}
	pad := got[ps1.HeaderSize+padAt : ps1.HeaderSize+padAt+150*ps1.SectorSize]
	for i, b := range pad {
		if b != 0 {
			t.Fatalf("pregap byte %d is %#02x, want 0", i, b)
		}
	}
	if !bytes.Equal(got[ps1.HeaderSize+padAt+150*ps1.SectorSize:], bin[padAt:]) {
		t.Error("data after the pregap does not match the source")
	}
	// No .partial file may survive a successful run.
	if _, err := os.Stat(out + ".partial"); !os.IsNotExist(err) {
		t.Error("a .partial file was left behind")
	}
}
