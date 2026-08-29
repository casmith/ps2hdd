package ps1

import (
	"errors"
	"fmt"
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
	BinPath   string
	GameID    string
	Title     string
	VolumeID  string
	SizeBytes int64
	// AudioTracks is the number of CD-DA tracks, which is what makes a rip
	// worth keeping as BIN/CUE rather than a bare image.
	AudioTracks int
	// DiscNumber is parsed from the filename, or 0 when the name says nothing.
	DiscNumber int
	// BootFile is the raw BOOT value from SYSTEM.CNF.
	BootFile string
}

// SourcePath is the path a user would name to install this disc.
func (d Disc) SourcePath() string {
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
	d.SizeBytes = fi.Size()

	vol, err := openPS1Volume(f, fi.Size())
	if err != nil {
		return d, fmt.Errorf("%s: %w: %v", filepath.Base(path), ErrNotPS1, err)
	}
	d.VolumeID = vol.VolumeID

	cnf, err := vol.ReadFile("SYSTEM.CNF")
	if err != nil {
		return d, fmt.Errorf("%s: %w: SYSTEM.CNF is missing", filepath.Base(path), ErrNotPS1)
	}
	boot, ok := parseKey(string(cnf), "BOOT")
	if !ok {
		return d, fmt.Errorf("%s: %w: SYSTEM.CNF has no BOOT entry", filepath.Base(path), ErrNotPS1)
	}
	d.BootFile = boot
	d.GameID = model.FindGameID(boot)
	if d.GameID == "" {
		return d, fmt.Errorf("%s: BOOT is %q, which carries no recognisable serial", filepath.Base(path), boot)
	}

	name := filepath.Base(path)
	d.DiscNumber = DiscNumber(name)
	d.Title = BaseTitle(name)
	return d, nil
}

// openPS1Volume tries the two sector layouts a PS1 rip can have.
//
// A BIN from a CD ripper is MODE2/2352 with 24 bytes of sync and headers in
// front of each 2048-byte block; an image converted to "ISO" is a plain 2048
// stream. Trying 2352 first matters because a 2048 read of a 2352 image can
// occasionally land on plausible-looking bytes.
func openPS1Volume(f *os.File, size int64) (*iso9660.Volume, error) {
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
