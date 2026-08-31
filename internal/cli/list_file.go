package cli

import (
	"bufio"
	"fmt"
	"os"
	"strings"
)

// ListEntry is one line of a game list, with where it came from so a line that
// resolves to nothing can be reported by number.
type ListEntry struct {
	Line  int
	Query string
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
