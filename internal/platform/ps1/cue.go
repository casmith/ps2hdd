// Package ps1 inspects PlayStation 1 disc images, converts them to the VCD
// format POPS expects, and manages POPStarter installations.
package ps1

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// ErrBadCue means the cuesheet could not be understood.
var ErrBadCue = errors.New("invalid cuesheet")

// MSF is a minutes:seconds:frames timecode. A CD runs at 75 frames per second.
type MSF struct{ M, S, F int }

// LBA converts a timecode to a logical block address.
func (t MSF) LBA() int { return t.M*60*75 + t.S*75 + t.F }

// String renders the standard mm:ss:ff form.
func (t MSF) String() string { return fmt.Sprintf("%02d:%02d:%02d", t.M, t.S, t.F) }

// BCD renders each component as a packed binary-coded decimal byte, which is
// how the POPS header stores timecodes.
func (t MSF) BCD() (m, s, f byte) {
	return bcd(t.M), bcd(t.S), bcd(t.F)
}

func bcd(v int) byte {
	if v < 0 {
		v = 0
	}
	return byte((v/10)<<4 | v%10)
}

// Track is one track of a cuesheet.
type Track struct {
	Number int
	// FileIndex is which FILE statement this track appeared under, counting
	// from zero. Always 0 in a single-file sheet; in a split dump it is what
	// turns a per-file INDEX into an absolute one.
	FileIndex int
	// Mode is the raw cuesheet mode, e.g. "MODE2/2352" or "AUDIO".
	Mode string
	// Index0 is the pregap position, if the sheet declares one.
	Index0    MSF
	HasIndex0 bool
	// Index1 is where the track's content starts.
	Index1 MSF
	// Pregap and Postgap record explicit PREGAP/POSTGAP commands.
	Pregap     MSF
	HasPregap  bool
	Postgap    MSF
	HasPostgap bool
}

// IsAudio reports whether the track holds CD-DA rather than data.
func (t Track) IsAudio() bool { return strings.EqualFold(t.Mode, "AUDIO") }

// Cue is a parsed cuesheet.
type Cue struct {
	// Path is the cuesheet's own path.
	Path string
	// BinPath is the absolute path of the single referenced data file.
	BinPath string
	// BinName is the FILE name exactly as written in the sheet.
	BinName string
	// FileType is the FILE type token, normally BINARY.
	FileType string
	Tracks   []Track
	// FileCount is how many FILE statements the sheet had. More than one is a
	// split dump: POPS needs a single stream, so Convert joins them.
	FileCount int
	// Files are every FILE name in the order the sheet lists them, and
	// FilePaths the same resolved against the sheet's directory. A single-file
	// sheet has one of each, matching BinName and BinPath.
	Files     []string
	FilePaths []string
}

// Split reports whether the rip is spread across more than one data file.
func (c Cue) Split() bool { return c.FileCount > 1 }

// AudioTracks reports how many tracks hold CD-DA.
func (c Cue) AudioTracks() int {
	n := 0
	for _, t := range c.Tracks {
		if t.IsAudio() {
			n++
		}
	}
	return n
}

// PregapCount and PostgapCount report explicit gap commands, which the VCD
// header's lead-out calculation depends on.
func (c Cue) PregapCount() int {
	n := 0
	for _, t := range c.Tracks {
		if t.HasPregap {
			n++
		}
	}
	return n
}

func (c Cue) PostgapCount() int {
	n := 0
	for _, t := range c.Tracks {
		if t.HasPostgap {
			n++
		}
	}
	return n
}

// ParseCueFile reads and parses a cuesheet from disk.
func ParseCueFile(path string) (Cue, error) {
	f, err := os.Open(path)
	if err != nil {
		return Cue{}, err
	}
	defer f.Close()
	c, err := ParseCue(f)
	if err != nil {
		return c, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	c.Path = path
	dir := filepath.Dir(path)
	for _, n := range c.Files {
		c.FilePaths = append(c.FilePaths, filepath.Join(dir, n))
	}
	if c.BinName != "" {
		c.BinPath = filepath.Join(dir, c.BinName)
	}
	return c, nil
}

// ParseCue parses cuesheet text.
//
// Only the subset that matters for PS1 images is understood: FILE, TRACK,
// INDEX, PREGAP and POSTGAP. Anything else (REM, TITLE, PERFORMER, CATALOG,
// FLAGS) is ignored rather than rejected, because real sheets carry plenty of
// it and none of it affects the conversion.
func ParseCue(r interface{ Read([]byte) (int, error) }) (Cue, error) {
	var c Cue
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	lineNo := 0
	for sc.Scan() {
		lineNo++
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		fields := splitCueLine(line)
		if len(fields) == 0 {
			continue
		}
		switch strings.ToUpper(fields[0]) {
		case "FILE":
			if len(fields) < 2 {
				return c, fmt.Errorf("%w: line %d: FILE has no filename", ErrBadCue, lineNo)
			}
			c.FileCount++
			c.Files = append(c.Files, fields[1])
			if c.FileCount == 1 {
				c.BinName = fields[1]
				if len(fields) >= 3 {
					c.FileType = strings.ToUpper(fields[2])
				}
			}
		case "TRACK":
			if len(fields) < 3 {
				return c, fmt.Errorf("%w: line %d: malformed TRACK", ErrBadCue, lineNo)
			}
			n, err := strconv.Atoi(fields[1])
			if err != nil {
				return c, fmt.Errorf("%w: line %d: track number %q", ErrBadCue, lineNo, fields[1])
			}
			c.Tracks = append(c.Tracks, Track{
				Number:    n,
				Mode:      strings.ToUpper(fields[2]),
				FileIndex: maxInt(c.FileCount-1, 0),
			})
		case "INDEX":
			if len(c.Tracks) == 0 {
				return c, fmt.Errorf("%w: line %d: INDEX before any TRACK", ErrBadCue, lineNo)
			}
			if len(fields) < 3 {
				return c, fmt.Errorf("%w: line %d: malformed INDEX", ErrBadCue, lineNo)
			}
			idx, err := strconv.Atoi(fields[1])
			if err != nil {
				return c, fmt.Errorf("%w: line %d: index number %q", ErrBadCue, lineNo, fields[1])
			}
			msf, err := ParseMSF(fields[2])
			if err != nil {
				return c, fmt.Errorf("%w: line %d: %v", ErrBadCue, lineNo, err)
			}
			t := &c.Tracks[len(c.Tracks)-1]
			switch idx {
			case 0:
				t.Index0, t.HasIndex0 = msf, true
			case 1:
				t.Index1 = msf
			}
		case "PREGAP", "POSTGAP":
			if len(c.Tracks) == 0 || len(fields) < 2 {
				continue
			}
			msf, err := ParseMSF(fields[1])
			if err != nil {
				return c, fmt.Errorf("%w: line %d: %v", ErrBadCue, lineNo, err)
			}
			t := &c.Tracks[len(c.Tracks)-1]
			if strings.EqualFold(fields[0], "PREGAP") {
				t.Pregap, t.HasPregap = msf, true
			} else {
				t.Postgap, t.HasPostgap = msf, true
			}
		}
	}
	if err := sc.Err(); err != nil {
		return c, err
	}
	if c.FileCount == 0 {
		return c, fmt.Errorf("%w: no FILE statement", ErrBadCue)
	}
	if len(c.Tracks) == 0 {
		return c, fmt.Errorf("%w: no TRACK statements", ErrBadCue)
	}
	return c, nil
}

// splitCueLine splits on whitespace while keeping quoted filenames together.
func splitCueLine(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	flush := func() {
		if cur.Len() > 0 {
			out = append(out, cur.String())
			cur.Reset()
		}
	}
	for _, r := range line {
		switch {
		case r == '"':
			inQuote = !inQuote
			if !inQuote {
				// A quoted field is complete even when empty.
				out = append(out, cur.String())
				cur.Reset()
			}
		case !inQuote && (r == ' ' || r == '\t'):
			flush()
		default:
			cur.WriteRune(r)
		}
	}
	flush()
	return out
}

// ParseMSF parses an mm:ss:ff timecode.
func ParseMSF(s string) (MSF, error) {
	parts := strings.Split(strings.TrimSpace(s), ":")
	if len(parts) != 3 {
		return MSF{}, fmt.Errorf("timecode %q is not mm:ss:ff", s)
	}
	var v [3]int
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil {
			return MSF{}, fmt.Errorf("timecode %q: %v", s, err)
		}
		v[i] = n
	}
	if v[1] > 59 || v[2] > 74 || v[0] < 0 || v[1] < 0 || v[2] < 0 {
		return MSF{}, fmt.Errorf("timecode %q is out of range", s)
	}
	return MSF{M: v[0], S: v[1], F: v[2]}, nil
}

// Validate checks that a cuesheet describes something POPS can play.
//
// POPS reads a raw MODE2/2352 stream, so the data track must be MODE2/2352 and
// the whole disc must live in one BIN. Split dumps (one file per track) and
// 2048-byte MODE1 dumps are rejected with an explanation rather than converted
// into an image that would not boot.
func (c Cue) Validate() error {
	if c.FileType != "" && c.FileType != "BINARY" {
		return fmt.Errorf("%w: FILE type is %s; POPS needs a BINARY image", ErrBadCue, c.FileType)
	}
	if len(c.Tracks) == 0 {
		return fmt.Errorf("%w: no tracks", ErrBadCue)
	}
	first := c.Tracks[0]
	if first.Mode != "MODE2/2352" {
		return fmt.Errorf("%w: track 1 is %s; POPS needs MODE2/2352 (a raw 2352-byte-per-sector rip)",
			ErrBadCue, first.Mode)
	}
	for _, t := range c.Tracks {
		if !t.IsAudio() && t.Mode != "MODE2/2352" && t.Mode != "MODE1/2352" {
			return fmt.Errorf("%w: track %d is %s, which is not a raw 2352-byte mode", ErrBadCue, t.Number, t.Mode)
		}
	}
	if c.BinPath != "" {
		fi, err := os.Stat(c.BinPath)
		if err != nil {
			return fmt.Errorf("%w: %s references %s, which is missing", ErrBadCue, filepath.Base(c.Path), c.BinName)
		}
		if fi.Size()%SectorSize != 0 {
			return fmt.Errorf("%w: %s is %d bytes, not a whole number of %d-byte sectors; the rip is truncated",
				ErrBadCue, c.BinName, fi.Size(), SectorSize)
		}
	}
	return nil
}

// SourceBytes totals every track file the sheet lists.
//
// Not the size of the file the first FILE line names: a Redump-style split
// dump keeps each track in its own BIN, and all of them go into the VCD. Using
// only the first understates a music-heavy title by most of the disc, which is
// how a space check can pass and the install then run out of room.
func (c Cue) SourceBytes() (int64, error) {
	paths := c.FilePaths
	if len(paths) == 0 && c.BinPath != "" {
		paths = []string{c.BinPath}
	}
	var total int64
	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			return 0, fmt.Errorf("%w: %s references %s, which is missing",
				ErrBadCue, filepath.Base(c.Path), filepath.Base(p))
		}
		total += fi.Size()
	}
	return total, nil
}

// GapSectors is how many sectors the conversion adds that are in no track
// file. A CDRWIN-style sheet declares its pregap rather than including it, so
// Convert materialises it and the VCD comes out that much larger.
func (c Cue) GapSectors() int {
	return leadInSectors * (c.PregapCount() + c.PostgapCount())
}

// LooksLikeCDRWIN reports the pregap convention CDRWIN-style sheets use: a
// single explicit PREGAP and no POSTGAP. cue2pops compensates for those dumps
// by inserting the pregap into the image, and so does the built-in converter.
func (c Cue) LooksLikeCDRWIN() bool {
	return c.PregapCount() == 1 && c.PostgapCount() == 0
}

// maxInt is the pre-generics helper this file needs in exactly one place.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// MSFFromLBA is the inverse of MSF.LBA.
func MSFFromLBA(lba int) MSF {
	if lba < 0 {
		lba = 0
	}
	m := lba / (60 * 75)
	rem := lba % (60 * 75)
	return MSF{M: m, S: rem / 75, F: rem % 75}
}

// Joined rewrites a split cuesheet as the single-file sheet it would be if its
// tracks were concatenated, given each file's size in bytes.
//
// A split rip stores every track in its own file with its own timecodes, and
// the two-second pregap before an audio track is real data inside that track's
// file -- Redump sheets say so explicitly with INDEX 00 00:00:00 followed by
// INDEX 01 00:02:00. Joining is therefore plain concatenation, and each
// track's absolute position is the running sector count of the files before it
// plus whatever its own sheet said.
//
// The sizes must be whole sectors. A track that is not is a truncated rip, and
// joining it would silently shift every track after it.
func (c Cue) Joined(sizes []int64) (Cue, error) {
	if len(sizes) != c.FileCount {
		return c, fmt.Errorf("%w: %s has %d files but %d sizes were given",
			ErrBadCue, filepath.Base(c.Path), c.FileCount, len(sizes))
	}
	starts := make([]int, len(sizes))
	running := 0
	for i, sz := range sizes {
		if sz%SectorSize != 0 {
			name := ""
			if i < len(c.Files) {
				name = c.Files[i]
			}
			return c, fmt.Errorf("%w: %s is %d bytes, not a whole number of %d-byte sectors; the rip is truncated",
				ErrBadCue, name, sz, SectorSize)
		}
		starts[i] = running
		running += int(sz / SectorSize)
	}

	out := c
	out.FileCount = 1
	out.Files = []string{c.BinName}
	out.FilePaths = nil
	out.Tracks = make([]Track, len(c.Tracks))
	for i, t := range c.Tracks {
		base := 0
		if t.FileIndex < len(starts) {
			base = starts[t.FileIndex]
		}
		t.Index1 = MSFFromLBA(base + t.Index1.LBA())
		if t.HasIndex0 {
			t.Index0 = MSFFromLBA(base + t.Index0.LBA())
		}
		t.FileIndex = 0
		out.Tracks[i] = t
	}
	return out, nil
}
