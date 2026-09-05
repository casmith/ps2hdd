package ps1

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// CloneCD writes a rip as three files: a .img holding raw 2352-byte sectors,
// a .sub holding subchannel data, and a .ccd describing the table of contents.
// The .img is the same shape POPS wants, so the only thing missing is the
// track layout a cuesheet would have carried -- and the .ccd has it, in more
// detail than a cuesheet does.
//
// A rip in this shape was being listed as installable and then failing in
// ParseCueFile, which read the whole 747 MB image looking for cuesheet text
// and gave up with "bufio.Scanner: token too long".
//
// The format is a Windows INI file:
//
//	[Entry 3]          the disc's table of contents, one section per TOC entry
//	Point=0x01         0xa0/0xa1/0xa2 are descriptors; 0x01..0x63 are tracks
//	Control=0x04       bit 2 set means a data track
//	PLBA=0             where it starts
//
//	[TRACK 1]          the tracks themselves
//	MODE=2             0 audio, 1 MODE1, 2 MODE2
//	INDEX 1=0
//
// Both halves describe the same tracks. The [TRACK] sections are used for the
// mode and the indexes because they carry INDEX 0 as well, and the [Entry]
// sections are used for the control flags, which say whether a track is audio
// when MODE does not.
const (
	ccdExt = ".ccd"
	ccdImg = ".img"
)

// ccdSection is one INI section's keys, lower-cased.
type ccdSection map[string]string

// ParseCCDFile reads a CloneCD control file and renders it as the cuesheet it
// would have been, so that everything downstream -- identification, size
// estimation and conversion -- works on a CloneCD rip unchanged.
func ParseCCDFile(path string) (Cue, error) {
	f, err := os.Open(path)
	if err != nil {
		return Cue{}, err
	}
	defer f.Close()

	sections := map[string]ccdSection{}
	var order []string
	cur := ""
	sc := bufio.NewScanner(f)
	// A control file is a few kilobytes; anything far larger is not one, and
	// the limit keeps a mistaken .ccd from being read as a whole disc.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			cur = strings.ToUpper(strings.TrimSpace(line[1 : len(line)-1]))
			if _, seen := sections[cur]; !seen {
				sections[cur] = ccdSection{}
				order = append(order, cur)
			}
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok || cur == "" {
			continue
		}
		sections[cur][strings.ToLower(strings.TrimSpace(k))] = strings.TrimSpace(v)
	}
	if err := sc.Err(); err != nil {
		return Cue{}, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	if _, ok := sections["CLONECD"]; !ok {
		return Cue{}, fmt.Errorf("%w: %s has no [CloneCD] section, so it is not a CloneCD control file",
			ErrBadCue, filepath.Base(path))
	}

	img, err := ccdImagePath(path)
	if err != nil {
		return Cue{}, err
	}

	// Control flags come from the TOC entries, keyed by track number.
	audio := map[int]bool{}
	for name, sec := range sections {
		if !strings.HasPrefix(name, "ENTRY ") {
			continue
		}
		point, err := parseCCDInt(sec["point"])
		if err != nil || point < 1 || point > 0x63 {
			continue // a descriptor (0xa0/0xa1/0xa2), not a track
		}
		ctrl, err := parseCCDInt(sec["control"])
		if err != nil {
			continue
		}
		// Bit 2 of the control field marks a data track; without it the track
		// is CD-DA.
		audio[point] = ctrl&0x04 == 0
	}

	c := Cue{
		Path:      path,
		BinPath:   img,
		BinName:   filepath.Base(img),
		FileType:  "BINARY",
		FileCount: 1,
		Files:     []string{filepath.Base(img)},
		FilePaths: []string{img},
	}
	var numbers []int
	for _, name := range order {
		if n, ok := strings.CutPrefix(name, "TRACK "); ok {
			if v, err := strconv.Atoi(strings.TrimSpace(n)); err == nil {
				numbers = append(numbers, v)
			}
		}
	}
	sort.Ints(numbers)
	for _, n := range numbers {
		sec := sections[fmt.Sprintf("TRACK %d", n)]
		t := Track{Number: n}
		mode, err := parseCCDInt(sec["mode"])
		if err != nil {
			return Cue{}, fmt.Errorf("%w: %s track %d has no usable MODE",
				ErrBadCue, filepath.Base(path), n)
		}
		switch {
		case mode == 0 || audio[n]:
			t.Mode = "AUDIO"
		case mode == 1:
			t.Mode = "MODE1/2352"
		case mode == 2:
			t.Mode = "MODE2/2352"
		default:
			return Cue{}, fmt.Errorf("%w: %s track %d is MODE %d, which is not a raw 2352-byte mode",
				ErrBadCue, filepath.Base(path), n, mode)
		}
		// CloneCD writes indexes as absolute LBAs, which is what a cuesheet's
		// INDEX means for a single-file sheet.
		if v, err := parseCCDInt(sec["index 0"]); err == nil {
			t.Index0, t.HasIndex0 = lbaToMSF(v), true
		}
		if v, err := parseCCDInt(sec["index 1"]); err == nil {
			t.Index1 = lbaToMSF(v)
		} else if t.HasIndex0 {
			t.Index1 = t.Index0
		}
		c.Tracks = append(c.Tracks, t)
	}
	if len(c.Tracks) == 0 {
		return Cue{}, fmt.Errorf("%w: %s lists no tracks", ErrBadCue, filepath.Base(path))
	}
	return c, nil
}

// ccdImagePath finds the .img beside a .ccd, tolerating the case the ripper
// happened to write.
func ccdImagePath(ccdPath string) (string, error) {
	stem := strings.TrimSuffix(ccdPath, filepath.Ext(ccdPath))
	for _, ext := range []string{ccdImg, ".IMG", ".Img"} {
		p := stem + ext
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p, nil
		}
	}
	// A directory listing is the only way to catch a stem that differs in
	// case as well; rare, but a missing image is otherwise reported as if the
	// rip were unreadable rather than incomplete.
	dir := filepath.Dir(ccdPath)
	want := strings.ToLower(filepath.Base(stem)) + ccdImg
	entries, err := os.ReadDir(dir)
	if err == nil {
		for _, e := range entries {
			if !e.IsDir() && strings.ToLower(e.Name()) == want {
				return filepath.Join(dir, e.Name()), nil
			}
		}
	}
	return "", fmt.Errorf("%w: %s has no .img beside it; a CloneCD rip needs the .ccd and the .img together",
		ErrBadCue, filepath.Base(ccdPath))
}

// parseCCDInt reads a CloneCD value, which may be decimal or 0x-prefixed hex.
func parseCCDInt(s string) (int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty")
	}
	if v, err := strconv.ParseInt(s, 0, 64); err == nil {
		return int(v), nil
	}
	return 0, fmt.Errorf("%q is not a number", s)
}
