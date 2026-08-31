package ps1

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/iso9660"
	"github.com/casmith/ps2hdd/internal/model"
)

// ErrNotPS1 means the image has no PlayStation boot record.
var ErrNotPS1 = errors.New("not a PlayStation 1 disc image")

// Disc is the result of inspecting one PS1 disc.
type Disc struct {
	// CuePath is the cuesheet, when the source is BIN/CUE. Empty for a
	// bare image.
	CuePath string
	// BinPath is the raw disc image.
	BinPath  string
	GameID   string
	Title    string
	VolumeID string
	// SizeBytes is the whole rip: every track file, not just the one the
	// first FILE line names.
	SizeBytes int64
	// VCDBytes is what the rip becomes once converted -- the POPS header, the
	// tracks and any materialised pregap. It is worked out here because this
	// is where the cuesheet is in hand.
	VCDBytes int64
	// AudioTracks is the number of CD-DA tracks, which is what makes a rip
	// worth keeping as BIN/CUE rather than a bare image.
	AudioTracks int
	// DiscNumber is parsed from the filename, or 0 when the name says nothing.
	DiscNumber int
	// BootFile is the raw BOOT value from SYSTEM.CNF.
	BootFile string
	// ArchivePath is the archive this disc lives inside, and ArchiveMember the
	// entry within it. Both empty for a rip that is loose on disk.
	//
	// They are carried on the disc rather than derived later because grouping
	// rebuilds games from discs, and anything not on the disc is lost in that
	// round trip.
	ArchivePath   string
	ArchiveMember string
}

// SourcePath is the path a user would name to install this disc.
func (d Disc) SourcePath() string {
	if d.ArchivePath != "" {
		return d.ArchivePath
	}
	if d.CuePath != "" {
		return d.CuePath
	}
	return d.BinPath
}

// Inspect identifies a PS1 disc from a .cue or a raw image path.
//
// Identity comes from SYSTEM.CNF inside the image, never from the filename:
// multi-disc releases regularly ship discs with different serials, and a
// filename-derived guess would merge or split titles wrongly. The filename is
// used only for the display title and the disc number.
func Inspect(path string) (Disc, error) {
	d := Disc{}
	ext := strings.ToLower(filepath.Ext(path))
	binPath := path

	var cue *Cue
	if ext == ".cue" {
		c, err := ParseCueFile(path)
		if err != nil {
			return d, err
		}
		if err := c.Validate(); err != nil {
			return d, err
		}
		d.CuePath = path
		d.AudioTracks = c.AudioTracks()
		binPath = c.BinPath
		cue = &c
	}
	d.BinPath = binPath

	f, err := os.Open(binPath)
	if err != nil {
		return d, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return d, err
	}
	// The data track's size is what the ISO reader must be given; the whole
	// rip's is what the install will cost. For a single-file sheet they are
	// the same, and for a split one they are not.
	total, gaps := fi.Size(), 0
	if cue != nil {
		if n, err := cue.SourceBytes(); err == nil {
			total = n
		}
		gaps = cue.GapSectors()
	}
	return identify(d, f, fi.Size(), total, gaps, path, false)
}

// InspectReader identifies a disc whose data is not on the filesystem.
//
// It exists for rips inside an archive, where the cuesheet has been read out
// as text and only the first few megabytes of the data track are available.
// That is enough: the volume descriptor and SYSTEM.CNF both sit at the front
// of a PS1 disc.
//
// cueText may be empty for a rip with no cuesheet. dataSize is the data
// track's full size, which the caller knows from the archive listing even
// though r covers only its beginning, and totalSize is every track in the rip.
// Passing 0 for totalSize means "the same as dataSize".
func InspectReader(cueText, name string, r io.ReaderAt, dataSize, totalSize int64) (Disc, error) {
	d := Disc{}
	gaps := 0
	if strings.TrimSpace(cueText) != "" {
		c, err := ParseCue(strings.NewReader(cueText))
		if err != nil {
			return d, fmt.Errorf("%s: %w", name, err)
		}
		if err := c.Validate(); err != nil {
			return d, fmt.Errorf("%s: %w", name, err)
		}
		d.AudioTracks = c.AudioTracks()
		gaps = c.GapSectors()
	}
	if totalSize <= 0 {
		totalSize = dataSize
	}
	return identify(d, r, dataSize, totalSize, gaps, name, true)
}

// partial says r holds only the beginning of the track, which is what a rip
// inside an archive gives: enough for the volume descriptor and the root
// directory, not necessarily enough for SYSTEM.CNF, whose contents can sit far
// into the disc. On a real library that is 252 discs in 2,100 -- CloneCD sets
// especially -- so the root directory is the fallback. It always holds the
// boot file, named for the serial.
//
// dataSize is the data track alone, which is what the ISO reader must be given;
// totalSize is every track, which is what the title weighs.
func identify(d Disc, r io.ReaderAt, dataSize, totalSize int64, gapSectors int, name string, partial bool) (Disc, error) {
	d.SizeBytes = totalSize
	d.VCDBytes = VCDSize(totalSize, gapSectors)

	vol, err := openPS1Volume(r, dataSize)
	if err != nil {
		return d, fmt.Errorf("%s: %w: %v", filepath.Base(name), ErrNotPS1, err)
	}
	d.VolumeID = vol.VolumeID

	cnf, cnfErr := vol.ReadFile("SYSTEM.CNF")
	switch {
	case cnfErr == nil:
		boot, ok := parseKey(string(cnf), "BOOT")
		if !ok {
			return d, fmt.Errorf("%s: %w: SYSTEM.CNF has no BOOT entry", filepath.Base(name), ErrNotPS1)
		}
		d.BootFile = boot
		d.GameID = model.FindGameID(boot)
		if d.GameID == "" {
			return d, fmt.Errorf("%s: BOOT is %q, which carries no recognisable serial", filepath.Base(name), boot)
		}
	case partial:
		id, err := serialFromRoot(vol)
		if err != nil {
			return d, fmt.Errorf("%s: %w: %v", filepath.Base(name), ErrNotPS1, err)
		}
		d.GameID = id
	default:
		return d, fmt.Errorf("%s: %w: SYSTEM.CNF is missing", filepath.Base(name), ErrNotPS1)
	}

	base := filepath.Base(name)
	d.DiscNumber = DiscNumber(base)
	d.Title = BaseTitle(base)
	return d, nil
}

// openPS1Volume tries the two sector layouts a PS1 rip can have.
//
// A BIN from a CD ripper is MODE2/2352 with 24 bytes of sync and headers in
// front of each 2048-byte block; an image converted to "ISO" is a plain 2048
// stream. Trying 2352 first matters because a 2048 read of a 2352 image can
// occasionally land on plausible-looking bytes.
func openPS1Volume(f io.ReaderAt, size int64) (*iso9660.Volume, error) {
	if size%SectorSize == 0 {
		if v, err := iso9660.Open(iso9660.Mode2352(f)); err == nil {
			return v, nil
		}
	}
	if v, err := iso9660.Open(iso9660.Mode2048(f)); err == nil {
		return v, nil
	}
	// Report the failure of the layout the file size suggests.
	if size%SectorSize == 0 {
		return iso9660.Open(iso9660.Mode2352(f))
	}
	return iso9660.Open(iso9660.Mode2048(f))
}

// parseKey reads a "KEY = value" line from SYSTEM.CNF.
func parseKey(text, key string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r\x00"))
		k, v, ok := strings.Cut(line, "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(k), key) {
			continue
		}
		return strings.TrimSpace(v), true
	}
	return "", false
}

// serialFromRoot reads the serial off the boot file in the root directory.
//
// It insists on exactly one match, because a disc with two serial-shaped names
// at its root is not something to guess about.
func serialFromRoot(vol *iso9660.Volume) (string, error) {
	names, err := vol.ReadDir()
	if err != nil {
		return "", fmt.Errorf("SYSTEM.CNF is out of reach and the root directory is unreadable: %w", err)
	}
	var found []string
	for _, n := range names {
		if id := model.BootFileSerial(n); id != "" {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("SYSTEM.CNF is out of reach and no boot file named for a serial is in the root directory")
	default:
		return "", fmt.Errorf("SYSTEM.CNF is out of reach and the root directory holds %d serial-shaped names (%s)",
			len(found), strings.Join(found, ", "))
	}
}
