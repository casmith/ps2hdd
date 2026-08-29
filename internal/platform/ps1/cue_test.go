package ps1_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

func TestParseCueMultitrack(t *testing.T) {
	c, err := ps1.ParseCueFile("../../../testdata/cue/multitrack.cue")
	if err != nil {
		t.Fatalf("ParseCueFile: %v", err)
	}
	if c.BinName != "game.bin" || c.FileType != "BINARY" || c.FileCount != 1 {
		t.Errorf("file = %q %q x%d", c.BinName, c.FileType, c.FileCount)
	}
	if len(c.Tracks) != 3 {
		t.Fatalf("tracks = %d, want 3", len(c.Tracks))
	}
	if c.Tracks[0].Mode != "MODE2/2352" || c.Tracks[0].IsAudio() {
		t.Errorf("track 1 = %+v", c.Tracks[0])
	}
	if !c.Tracks[1].IsAudio() {
		t.Error("track 2 should be audio")
	}
	if !c.Tracks[1].HasIndex0 {
		t.Error("track 2 has an INDEX 00 that was not recorded")
	}
	if got := c.Tracks[1].Index0; got != (ps1.MSF{M: 3, S: 20, F: 0}) {
		t.Errorf("track 2 index0 = %v", got)
	}
	if got := c.Tracks[1].Index1; got != (ps1.MSF{M: 3, S: 22, F: 0}) {
		t.Errorf("track 2 index1 = %v", got)
	}
	if c.AudioTracks() != 2 {
		t.Errorf("AudioTracks = %d, want 2", c.AudioTracks())
	}
	if c.LooksLikeCDRWIN() {
		t.Error("a sheet with no PREGAP is not CDRWIN-style")
	}
}

func TestParseCueCDRWIN(t *testing.T) {
	c, err := ps1.ParseCueFile("../../../testdata/cue/cdrwin.cue")
	if err != nil {
		t.Fatal(err)
	}
	if c.PregapCount() != 1 || c.PostgapCount() != 0 {
		t.Errorf("gaps = %d pre, %d post", c.PregapCount(), c.PostgapCount())
	}
	if !c.LooksLikeCDRWIN() {
		t.Error("LooksLikeCDRWIN = false for a single-PREGAP sheet")
	}
}

func TestParseCueQuotedFilenameWithSpaces(t *testing.T) {
	c, err := ps1.ParseCue(strings.NewReader(
		"FILE \"Final Fantasy VII (Disc 1).bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"))
	if err != nil {
		t.Fatal(err)
	}
	if c.BinName != "Final Fantasy VII (Disc 1).bin" {
		t.Errorf("BinName = %q", c.BinName)
	}
}

func TestParseCueIgnoresDecoration(t *testing.T) {
	// Real sheets carry REM, CATALOG, TITLE and FLAGS lines. None affect the
	// conversion, so none may cause a parse failure.
	const sheet = `REM GENRE RPG
CATALOG 0000000000000
TITLE "Some Game"
PERFORMER "Someone"
FILE "g.bin" BINARY
  TRACK 01 MODE2/2352
    FLAGS DCP
    INDEX 01 00:00:00
`
	c, err := ps1.ParseCue(strings.NewReader(sheet))
	if err != nil {
		t.Fatalf("ParseCue: %v", err)
	}
	if len(c.Tracks) != 1 {
		t.Errorf("tracks = %d", len(c.Tracks))
	}
}

func TestParseCueErrors(t *testing.T) {
	for name, sheet := range map[string]string{
		"no file":       "TRACK 01 MODE2/2352\n  INDEX 01 00:00:00\n",
		"no track":      "FILE \"g.bin\" BINARY\n",
		"index first":   "FILE \"g.bin\" BINARY\n  INDEX 01 00:00:00\n",
		"bad timecode":  "FILE \"g.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 99:99:99\n",
		"short index":   "FILE \"g.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01\n",
		"bad track num": "FILE \"g.bin\" BINARY\n  TRACK xx MODE2/2352\n    INDEX 01 00:00:00\n",
	} {
		if _, err := ps1.ParseCue(strings.NewReader(sheet)); err == nil {
			t.Errorf("%s: ParseCue accepted an invalid sheet", name)
		}
	}
}

func TestCueValidateRejectsSplitDump(t *testing.T) {
	const sheet = `FILE "t1.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "t2.bin" BINARY
  TRACK 02 AUDIO
    INDEX 01 00:00:00
`
	c, err := ps1.ParseCue(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	if !errors.Is(err, ps1.ErrBadCue) {
		t.Fatalf("err = %v, want ErrBadCue", err)
	}
	if !strings.Contains(err.Error(), "single BIN") {
		t.Errorf("error should explain what POPS needs: %v", err)
	}
}

func TestCueValidateRejectsMode1_2048(t *testing.T) {
	// A 2048-byte-per-sector rip has no room for the raw subchannel data POPS
	// expects, so it must be rejected rather than converted into an image that
	// would not boot.
	c, err := ps1.ParseCue(strings.NewReader(
		"FILE \"g.bin\" BINARY\n  TRACK 01 MODE1/2048\n    INDEX 01 00:00:00\n"))
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	if err == nil || !strings.Contains(err.Error(), "MODE2/2352") {
		t.Fatalf("err = %v, want a MODE2/2352 complaint", err)
	}
}

func TestCueValidateRejectsTruncatedBin(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "g.bin"), make([]byte, 2352*10+11), 0o600); err != nil {
		t.Fatal(err)
	}
	cue := filepath.Join(dir, "g.cue")
	if err := os.WriteFile(cue, []byte("FILE \"g.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ps1.ParseCueFile(cue)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "truncated") {
		t.Fatalf("err = %v, want a truncation complaint", err)
	}
}

func TestCueValidateRejectsMissingBin(t *testing.T) {
	dir := t.TempDir()
	cue := filepath.Join(dir, "g.cue")
	if err := os.WriteFile(cue, []byte("FILE \"absent.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ps1.ParseCueFile(cue)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("err = %v, want a missing-file complaint", err)
	}
}

func TestMSF(t *testing.T) {
	m, err := ps1.ParseMSF("03:20:15")
	if err != nil {
		t.Fatal(err)
	}
	if m.LBA() != 3*4500+20*75+15 {
		t.Errorf("LBA = %d", m.LBA())
	}
	if m.String() != "03:20:15" {
		t.Errorf("String = %q", m.String())
	}
	bm, bs, bf := m.BCD()
	if bm != 0x03 || bs != 0x20 || bf != 0x15 {
		t.Errorf("BCD = %02x:%02x:%02x", bm, bs, bf)
	}
	for _, bad := range []string{"1:2", "aa:bb:cc", "00:60:00", "00:00:75", ""} {
		if _, err := ps1.ParseMSF(bad); err == nil {
			t.Errorf("ParseMSF(%q) accepted an invalid timecode", bad)
		}
	}
}
