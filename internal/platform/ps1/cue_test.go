package ps1_test

import (
	"errors"
	"fmt"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
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

// A split dump is no longer a dead end: Convert joins the tracks, so the
// sheet has to validate and to report which file each track came from.
func TestCueParsesSplitDump(t *testing.T) {
	const sheet = `FILE "t1.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "t2.bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
`
	c, err := ps1.ParseCue(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Validate(); err != nil {
		t.Fatalf("a split dump no longer fails validation: %v", err)
	}
	if !c.Split() || c.FileCount != 2 {
		t.Errorf("Split=%v FileCount=%d, want true and 2", c.Split(), c.FileCount)
	}
	if got := []string{c.Files[0], c.Files[1]}; got[0] != "t1.bin" || got[1] != "t2.bin" {
		t.Errorf("Files = %v", got)
	}
	if c.Tracks[0].FileIndex != 0 || c.Tracks[1].FileIndex != 1 {
		t.Errorf("track file indexes = %d, %d; want 0, 1",
			c.Tracks[0].FileIndex, c.Tracks[1].FileIndex)
	}
}

// Joining is what makes a split rip installable, and getting the arithmetic
// wrong shifts every track after the first -- audio that plays from the wrong
// place, which is not something a user would trace back to here.
func TestCueJoined(t *testing.T) {
	const sheet = `FILE "t1.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "t2.bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
FILE "t3.bin" BINARY
  TRACK 03 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
`
	c, err := ps1.ParseCue(strings.NewReader(sheet))
	if err != nil {
		t.Fatal(err)
	}
	// Real track sizes from a Redump rip, all whole sectors.
	sizes := []int64{3194016, 43674288, 45518256}
	j, err := c.Joined(sizes)
	if err != nil {
		t.Fatalf("Joined: %v", err)
	}
	if j.Split() {
		t.Error("the joined sheet still reports as split")
	}
	// 3194016/2352 = 1358 sectors, then +150 for the pregap inside track 2.
	want := []string{"00:00:00", "00:20:08", "04:27:52"}
	for i, w := range want {
		if got := j.Tracks[i].Index1.String(); got != w {
			t.Errorf("track %d INDEX 01 = %s, want %s", i+1, got, w)
		}
	}
	// INDEX 00 marks the start of the pregap, which is the file boundary.
	if got := j.Tracks[1].Index0.String(); got != "00:18:08" {
		t.Errorf("track 2 INDEX 00 = %s, want 00:18:08", got)
	}
}

// A track that is not a whole number of sectors is a truncated rip. Joining it
// anyway would silently shift everything after it.
func TestCueJoinedRejectsTruncatedTracks(t *testing.T) {
	const sheet = `FILE "t1.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "t2.bin" BINARY
  TRACK 02 AUDIO
    INDEX 01 00:00:00
`
	c, _ := ps1.ParseCue(strings.NewReader(sheet))
	if _, err := c.Joined([]int64{3194016, 12345}); !errors.Is(err, ps1.ErrBadCue) {
		t.Fatalf("err = %v, want ErrBadCue", err)
	}
	if _, err := c.Joined([]int64{3194016}); err == nil {
		t.Error("a size list shorter than the file count was accepted")
	}
}

func TestMSFFromLBARoundTrips(t *testing.T) {
	for _, lba := range []int{0, 1, 74, 75, 150, 4499, 4500, 92610, 333000} {
		if got := ps1.MSFFromLBA(lba).LBA(); got != lba {
			t.Errorf("MSFFromLBA(%d).LBA() = %d", lba, got)
		}
	}
	if got := ps1.MSFFromLBA(92610).String(); got != "20:34:60" {
		t.Errorf("MSFFromLBA(92610) = %s, want 20:34:60", got)
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

// writeRip lays down a cuesheet and its track files, each a whole number of
// sectors, and returns the cuesheet's path.
func writeRip(t *testing.T, sheet string, trackSectors ...int) string {
	t.Helper()
	dir := t.TempDir()
	for i, sectors := range trackSectors {
		name := filepath.Join(dir, fmt.Sprintf("track%02d.bin", i+1))
		if err := os.WriteFile(name, make([]byte, sectors*ps1.SectorSize), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	p := filepath.Join(dir, "rip.cue")
	if err := os.WriteFile(p, []byte(sheet), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A split dump's weight is all of its tracks. Reading only the file the first
// FILE line names is what made a space check pass and the install then run out
// of room, and on a music-heavy title the two differ by most of the disc.
func TestSourceBytesTotalsEveryTrack(t *testing.T) {
	sheet := `FILE "track01.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "track02.bin" BINARY
  TRACK 02 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
FILE "track03.bin" BINARY
  TRACK 03 AUDIO
    INDEX 00 00:00:00
    INDEX 01 00:02:00
`
	c, err := ps1.ParseCueFile(writeRip(t, sheet, 10, 300, 500))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.SourceBytes()
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(810 * ps1.SectorSize); got != want {
		t.Errorf("got %d bytes, want %d (the first track alone is %d)", got, want, 10*ps1.SectorSize)
	}
}

func TestSourceBytesOnASingleFileSheet(t *testing.T) {
	sheet := `FILE "track01.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
`
	c, err := ps1.ParseCueFile(writeRip(t, sheet, 400))
	if err != nil {
		t.Fatal(err)
	}
	got, err := c.SourceBytes()
	if err != nil {
		t.Fatal(err)
	}
	if want := int64(400 * ps1.SectorSize); got != want {
		t.Errorf("got %d, want %d", got, want)
	}
}

// A missing track has to be reported rather than silently totalling to less.
func TestSourceBytesReportsAMissingTrack(t *testing.T) {
	sheet := `FILE "track01.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
FILE "gone.bin" BINARY
  TRACK 02 AUDIO
    INDEX 01 00:00:00
`
	c, err := ps1.ParseCueFile(writeRip(t, sheet, 10))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.SourceBytes(); err == nil {
		t.Fatal("a sheet referencing a missing track totalled without complaint")
	}
}

// The VCD is the POPS header, the tracks, and any gap the conversion
// materialises for a CDRWIN-style sheet. Every term matters: dropping the
// header understates every title by a megabyte, and dropping the gap
// understates the sheets that declare a pregap rather than including it.
func TestVCDSizeAccountsForHeaderAndGap(t *testing.T) {
	const tracks = int64(1000 * ps1.SectorSize)
	if got, want := ps1.VCDSize(tracks, 0), int64(ps1.HeaderSize)+tracks; got != want {
		t.Errorf("no gap: got %d, want %d", got, want)
	}
	if got, want := ps1.VCDSize(tracks, 150), int64(ps1.HeaderSize)+tracks+150*ps1.SectorSize; got != want {
		t.Errorf("one pregap: got %d, want %d", got, want)
	}
	if got := ps1.VCDSize(0, 0); got != 0 {
		t.Errorf("an empty rip was charged %d bytes", got)
	}
}

// GapSectors counts the lead-in the conversion inserts, which is what makes
// the predicted VCD match the one Convert writes. The lead-in is 150 frames.
func TestGapSectorsMatchesTheConversion(t *testing.T) {
	plain, err := ps1.ParseCue(strings.NewReader(`FILE "a.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := plain.GapSectors(); got != 0 {
		t.Errorf("a sheet with no pregap reported %d gap sectors", got)
	}
	cdrwin, err := ps1.ParseCue(strings.NewReader(`FILE "a.bin" BINARY
  TRACK 01 MODE2/2352
    INDEX 01 00:00:00
  TRACK 02 AUDIO
    PREGAP 00:02:00
    INDEX 01 00:10:00
`))
	if err != nil {
		t.Fatal(err)
	}
	if got := cdrwin.GapSectors(); got != 150 {
		t.Errorf("a CDRWIN sheet reported %d gap sectors, want 150", got)
	}
}

// Cuesheets are routinely written on Windows, where the case of a filename is
// not information. This one is from a 2003 rip that works everywhere except a
// case-sensitive filesystem:
//
//	FILE "FINAL FANTASY VII DISC 1.BIN" BINARY
//
// beside a file called "Final Fantasy VII Disc 1.bin".
func TestParseCueFileMatchesTheFilenameCaseInsensitively(t *testing.T) {
	dir := t.TempDir()
	img, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: "SCUS_941.63",
		CDXA:     true,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF("SCUS_941.63")},
	})
	if err != nil {
		t.Fatal(err)
	}
	const real = "Final Fantasy VII Disc 1.bin"
	if err := os.WriteFile(filepath.Join(dir, real), img, 0o600); err != nil {
		t.Fatal(err)
	}
	cue := filepath.Join(dir, "Final Fantasy VII Disc 1.cue")
	if err := os.WriteFile(cue, []byte(
		"FILE \"FINAL FANTASY VII DISC 1.BIN\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	c, err := ps1.ParseCueFile(cue)
	if err != nil {
		t.Fatalf("ParseCueFile: %v", err)
	}
	if filepath.Base(c.BinPath) != real {
		t.Errorf("BinPath = %q, want the file that is actually there (%q)", c.BinPath, real)
	}
	if len(c.FilePaths) != 1 || filepath.Base(c.FilePaths[0]) != real {
		t.Errorf("FilePaths = %v, want [%q]", c.FilePaths, real)
	}
	// BinName keeps what the sheet said: it is what an error message should
	// quote, and what the archive's own listing will agree with.
	if c.BinName != "FINAL FANTASY VII DISC 1.BIN" {
		t.Errorf("BinName = %q, want the name as written in the sheet", c.BinName)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("a rip whose only fault is the case of a filename was rejected: %v", err)
	}
}

// An exact match is always preferred, so a directory holding two files that
// differ only in case still resolves to the one the sheet names.
func TestParseCueFilePrefersTheExactFilename(t *testing.T) {
	dir := t.TempDir()
	sectors := make([]byte, 2352*4)
	for _, n := range []string{"game.bin", "GAME.BIN"} {
		if err := os.WriteFile(filepath.Join(dir, n), sectors, 0o600); err != nil {
			t.Skipf("this filesystem cannot hold two names differing only in case: %v", err)
		}
	}
	cue := filepath.Join(dir, "game.cue")
	if err := os.WriteFile(cue, []byte(
		"FILE \"GAME.BIN\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ps1.ParseCueFile(cue)
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(c.BinPath) != "GAME.BIN" {
		t.Errorf("BinPath = %q, want the exactly named GAME.BIN", c.BinPath)
	}
}

// A file that is genuinely absent must still be reported, quoting the name the
// sheet used rather than something invented while looking for it.
func TestParseCueFileStillReportsAMissingTrack(t *testing.T) {
	dir := t.TempDir()
	cue := filepath.Join(dir, "game.cue")
	if err := os.WriteFile(cue, []byte(
		"FILE \"absent.bin\" BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := ps1.ParseCueFile(cue)
	if err != nil {
		t.Fatal(err)
	}
	err = c.Validate()
	if err == nil {
		t.Fatal("a cuesheet naming a file that does not exist was accepted")
	}
	if !strings.Contains(err.Error(), "absent.bin") {
		t.Errorf("the error does not name the missing file: %v", err)
	}
}
