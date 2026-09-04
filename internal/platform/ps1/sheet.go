package ps1

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Rip shapes a PS1 library actually arrives in, beyond BIN/CUE.
const (
	nrgExt = ".nrg"
	// rawSectorHeader is the sync, header and subheader a 2352-byte sector
	// carries before its user data.
	rawSectorHeader = 24
)

// LoadSheet reads the track layout of a rip, whatever shape it arrives in.
//
// POPS wants one thing: raw 2352-byte sectors, with a table of contents. A
// BIN/CUE says so directly, a CloneCD .ccd says the same in a different
// notation, and a bare image says nothing at all but can be checked. Anything
// that cannot supply it is refused here, by name, rather than further down
// where the failure is unrecognisable -- reading a 747 MB image as cuesheet
// text used to end in "bufio.Scanner: token too long".
func LoadSheet(path string) (Cue, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".cue":
		return ParseCueFile(path)
	case ccdExt:
		return ParseCCDFile(path)
	case nrgExt:
		return Cue{}, fmt.Errorf("%w: %s is a Nero image, which ps2hdd cannot convert: "+
			"its sectors are stored without the 16-byte sync and header POPS needs. "+
			"Convert it to BIN/CUE first", ErrUnsupportedRip, filepath.Base(path))
	default:
		return sheetForRawImage(path)
	}
}

// ErrUnsupportedRip is returned for a rip in a shape that cannot become a VCD.
var ErrUnsupportedRip = fmt.Errorf("unsupported rip")

// sheetForRawImage builds the sheet a bare raw image never came with.
//
// Nothing is guessed. The file has to be a whole number of 2352-byte sectors
// and carry the ISO 9660 volume descriptor where a MODE2/2352 rip keeps it --
// twenty-four bytes into sector 16 -- before a single data track is assumed.
// An image that is not that shape is refused by name.
func sheetForRawImage(path string) (Cue, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return Cue{}, err
	}
	name := filepath.Base(path)
	if fi.Size() == 0 || fi.Size()%SectorSize != 0 {
		return Cue{}, fmt.Errorf("%w: %s is %d bytes, which is not a whole number of %d-byte sectors, "+
			"so it is not a raw MODE2/2352 rip. POPS cannot use a 2048-byte-per-sector image",
			ErrUnsupportedRip, name, fi.Size(), SectorSize)
	}
	f, err := os.Open(path)
	if err != nil {
		return Cue{}, err
	}
	defer f.Close()
	buf := make([]byte, 6)
	if _, err := f.ReadAt(buf, 16*SectorSize+rawSectorHeader); err != nil || string(buf) != "\x01CD001" {
		return Cue{}, fmt.Errorf("%w: %s has no ISO 9660 volume descriptor where a MODE2/2352 rip keeps it, "+
			"so its sector layout is not one POPS can read", ErrUnsupportedRip, name)
	}
	return Cue{
		Path:      path,
		BinPath:   path,
		BinName:   name,
		FileType:  "BINARY",
		FileCount: 1,
		Files:     []string{name},
		FilePaths: []string{path},
		Tracks:    []Track{{Number: 1, Mode: "MODE2/2352"}},
	}, nil
}

// CCDCompanions returns the files a CloneCD control file speaks for: the image
// holding the sectors and the subchannel dump beside it.
//
// The scanner needs them so that a .ccd is the one entry point for the rip and
// its .img is not also listed as a title in its own right.
func CCDCompanions(ccdPath string) []string {
	stem := strings.TrimSuffix(ccdPath, filepath.Ext(ccdPath))
	var out []string
	if img, err := ccdImagePath(ccdPath); err == nil {
		out = append(out, img)
	}
	for _, ext := range []string{".sub", ".SUB"} {
		if _, err := os.Stat(stem + ext); err == nil {
			out = append(out, stem+ext)
		}
	}
	return out
}
