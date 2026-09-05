package ps1_test

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// dataTrack builds a PS1 data track: a real MODE2/2352 image with SYSTEM.CNF.
func dataTrack(t *testing.T, serial string) []byte {
	t.Helper()
	b, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: serial,
		CDXA:     true,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF(serial)},
	})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// writeCloneCD lays down a CloneCD rip: the .ccd control file, the .img of raw
// sectors, and the .sub a ripper leaves beside them.
func writeCloneCD(t *testing.T, dir, stem, ccd string, img []byte) string {
	t.Helper()
	base := filepath.Join(dir, stem)
	if err := os.WriteFile(base+".ccd", []byte(ccd), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".img", img, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(base+".sub", make([]byte, 96*len(img)/2352), 0o600); err != nil {
		t.Fatal(err)
	}
	return base + ".ccd"
}

// singleTrackCCD is the control file a single-track PS1 disc gets, in the shape
// CloneCD writes: a TOC of A0/A1/A2 descriptors and one track entry.
func singleTrackCCD(sectors int) string {
	return fmt.Sprintf(`[CloneCD]
Version=3

[Disc]
TocEntries=4
Sessions=1
DataTracksScrambled=0
CDTextLength=0

[Entry 0]
Session=1
Point=0xa0
Control=0x04
PMin=1
PSec=32
PLBA=6750

[Entry 1]
Session=1
Point=0xa1
Control=0x04
PMin=1
PLBA=4350

[Entry 2]
Session=1
Point=0xa2
Control=0x04
PLBA=%d

[Entry 3]
Session=1
Point=0x01
Control=0x04
PLBA=0

[TRACK 1]
MODE=2
INDEX 1=0
`, sectors)
}

// A CloneCD rip carries everything a cuesheet does, so it must convert to the
// same VCD a BIN/CUE of the same disc would. The .img is already the raw
// 2352-byte stream POPS wants; only the table of contents was missing.
func TestConvertCloneCDMatchesBinCue(t *testing.T) {
	dir := t.TempDir()
	img := dataTrack(t, "SLUS_004.11")

	ccdPath := writeCloneCD(t, dir, "game", singleTrackCCD(len(img)/2352), img)

	// The same disc as BIN/CUE, which the converter is already proven on.
	if err := os.WriteFile(filepath.Join(dir, "game.bin"), img, 0o600); err != nil {
		t.Fatal(err)
	}
	cuePath := filepath.Join(dir, "game.cue")
	if err := os.WriteFile(cuePath,
		[]byte("FILE \"game.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	fromCCD := filepath.Join(dir, "from-ccd.vcd")
	fromCue := filepath.Join(dir, "from-cue.vcd")
	if err := ps1.Convert(ccdPath, fromCCD, ps1.ConvertOptions{}); err != nil {
		t.Fatalf("convert the CloneCD rip: %v", err)
	}
	if err := ps1.Convert(cuePath, fromCue, ps1.ConvertOptions{}); err != nil {
		t.Fatalf("convert the BIN/CUE rip: %v", err)
	}
	a, err := os.ReadFile(fromCCD)
	if err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(fromCue)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a, b) {
		if !bytes.Equal(a[:ps1.HeaderSize], b[:ps1.HeaderSize]) {
			t.Fatal("the POPS header built from the .ccd differs from the one built from the cuesheet")
		}
		t.Fatal("the disc data differs between the CloneCD and BIN/CUE conversions")
	}
}

// The control field, not the MODE line, is what marks a track as CD-DA.
func TestParseCCDReadsTrackModesAndAudio(t *testing.T) {
	dir := t.TempDir()
	img := append(dataTrack(t, "SLUS_004.12"), bytes.Repeat([]byte{0x7f}, 2352*300)...)
	ccd := singleTrackCCD(len(img)/2352) + `
[Entry 4]
Session=1
Point=0x02
Control=0x00
PLBA=1000

[TRACK 2]
MODE=0
INDEX 0=985
INDEX 1=1000
`
	ccdPath := writeCloneCD(t, dir, "mixed", ccd, img)

	c, err := ps1.ParseCCDFile(ccdPath)
	if err != nil {
		t.Fatalf("ParseCCDFile: %v", err)
	}
	if len(c.Tracks) != 2 {
		t.Fatalf("got %d tracks, want 2", len(c.Tracks))
	}
	if c.Tracks[0].Mode != "MODE2/2352" {
		t.Errorf("track 1 mode = %q", c.Tracks[0].Mode)
	}
	if !c.Tracks[1].IsAudio() {
		t.Errorf("track 2 mode = %q, want AUDIO: its control field has the data bit clear", c.Tracks[1].Mode)
	}
	// CloneCD writes absolute LBAs, which is what a single-file cuesheet's
	// INDEX means. 1000 sectors is 00:13:25.
	if got := c.Tracks[1].Index1; got.M != 0 || got.S != 13 || got.F != 25 {
		t.Errorf("track 2 INDEX 01 = %v, want 00:13:25", got)
	}
	if !c.Tracks[1].HasIndex0 {
		t.Error("track 2 lost its INDEX 00")
	}
	if c.BinName != "mixed.img" {
		t.Errorf("bin = %q, want the .img beside the .ccd", c.BinName)
	}
}

// A .ccd without its .img is an incomplete rip, and saying so is the whole
// point: the old failure read the image as cuesheet text and reported
// "bufio.Scanner: token too long".
func TestCCDWithoutImageSaysSo(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "lonely.ccd")
	if err := os.WriteFile(p, []byte(singleTrackCCD(100)), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ps1.ParseCCDFile(p)
	if err == nil {
		t.Fatal("a .ccd with no image was accepted")
	}
	if !strings.Contains(err.Error(), ".img") {
		t.Errorf("error does not say what is missing: %v", err)
	}
}

// A Nero image cannot become a VCD -- its sectors are stored without the sync
// and header POPS needs -- and it has to fail by name rather than deep inside
// a cuesheet parser.
func TestNeroIsRefusedByName(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "game.nrg")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0}, 4096), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := ps1.LoadSheet(p)
	if !errors.Is(err, ps1.ErrUnsupportedRip) {
		t.Fatalf("err = %v, want ErrUnsupportedRip", err)
	}
	for _, want := range []string{"Nero", "BIN/CUE"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not mention %q: %v", want, err)
		}
	}
}

// A bare raw image with no control file at all is still convertible: it is
// already the shape POPS wants, and the single data track can be checked
// rather than assumed.
func TestLoadSheetSynthesisesASheetForABareRawImage(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "bare.img")
	if err := os.WriteFile(p, dataTrack(t, "SLUS_004.13"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ps1.LoadSheet(p)
	if err != nil {
		t.Fatalf("LoadSheet: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("the synthesised sheet is not valid: %v", err)
	}
	if len(c.Tracks) != 1 || c.Tracks[0].Mode != "MODE2/2352" {
		t.Errorf("tracks = %+v", c.Tracks)
	}
}

// An image that is not raw 2352 is refused, and never guessed at. A 2048-byte
// per sector dump is the common case and cannot be used.
func TestLoadSheetRefusesAnImageThatIsNotRaw(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "cooked.iso")
	if err := os.WriteFile(p, bytes.Repeat([]byte{0}, 2048*32), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := ps1.LoadSheet(p); !errors.Is(err, ps1.ErrUnsupportedRip) {
		t.Fatalf("err = %v, want ErrUnsupportedRip", err)
	}
}
