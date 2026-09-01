package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// ListEntry is one line of a game list, with where it came from so a line that
// resolves to nothing can be reported by number.
type ListEntry struct {
	Line  int
	Query string
}

// serialIn pulls the game id out of a line that carries one among other text.
//
// A list is very often assembled by pasting rows straight out of `ps2hdd list`,
// which is a reasonable way to build one and produces lines like
//
//	PS2  Arc the Lad - End of Darkness  SLUS_211.65  3.0 GiB  Installed + source
//
// Taken whole that matches no title, no filename and no serial -- but the
// serial is right there in it, and a serial names exactly one game.
//
// It is the last thing tried, never the first. An exact title or filename is a
// better answer than a token found by scanning, so only a line that nothing
// else could place is searched this way.
func serialIn(query string) string {
	id := model.FindGameID(query)
	if id == "" {
		return ""
	}
	// A line that is nothing but the serial already resolves directly, and the
	// comparison has to be between like and like: SLUS-21165 and SLUS_211.65
	// are the same serial written two ways, and only normalising both says so.
	if model.NormalizeGameID(id) == model.NormalizeGameID(query) {
		return ""
	}
	return id
}

// readGameList parses a list of titles to install.
//
// One entry per line: a title, a serial, or a path. Blank lines are skipped
// and everything from a '#' to the end of a line is a comment, so a list can
// carry notes and be kept under version control alongside whatever produced
// it. Leading and trailing space is trimmed, since a list is usually generated
// or hand-edited and neither is careful about it.
func readGameList(path string) ([]ListEntry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var out []ListEntry
	sc := bufio.NewScanner(f)
	// A line is a game name, not a file; the default 64 KiB is ample and the
	// cap keeps a binary handed over by mistake from being read into memory.
	sc.Buffer(make([]byte, 0, 4096), 1<<16)
	for n := 1; sc.Scan(); n++ {
		line := sc.Text()
		if i := strings.IndexByte(line, '#'); i >= 0 {
			line = line[:i]
		}
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		out = append(out, ListEntry{Line: n, Query: line})
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("%s names no games", path)
	}
	return out, nil
}
