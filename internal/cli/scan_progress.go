package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/term"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/catalog"
)

// scanRedrawInterval bounds how often the progress line is repainted. A cached
// rescan resolves thousands of files in a second, and repainting for every one
// of them costs more than the scan does.
const scanRedrawInterval = 50 * time.Millisecond

// scanPrinter renders the position of a source scan on one line, redrawing in
// place, and returns a second function that clears it when the scan is done.
//
// Scanning is the slowest thing ps2hdd does -- a library of two thousand
// archives has to be opened one at a time -- and a spinner reports only that
// the process is alive. This reports where in the library it has got to, in
// the alphabetical order the files were collected in, so a slow scan can be
// told apart from a stuck one.
//
// The line goes to stderr, never stdout: `list --json` pipes its result, and a
// progress line in the middle of that would corrupt it. When output is not a
// terminal nothing is printed at all, so a pipe or a log file does not collect
// thousands of redraws.
//
// A nil report is the documented way to ask for no progress at all, so the
// caller passes the result straight into app.ScanOptions without a check.
func scanPrinter(env *Env) (report func(catalog.ScanProgress), finish func()) {
	if env.Quiet || !colorEnabled {
		return nil, func() {}
	}
	var (
		painted bool
		lastAt  time.Time
	)
	width := terminalWidth()
	paint := func(p catalog.ScanProgress) {
		label := scanLabel(env, p.Root)
		// head is the visible line without the filename. Colour is applied
		// below and adds no width, so this is what decides the room left.
		head := fmt.Sprintf("  scanning %s %d/%d  ", label, p.Done, p.Total)
		// The line must fit: it is redrawn in place, and a line that wraps
		// leaves its overflow behind on every repaint instead of being
		// overwritten.
		name := ""
		if room := width - len([]rune(head)) - 1; room >= 8 {
			name = truncateMiddle(filepath.Base(p.Path), room)
		}
		fmt.Fprintf(env.ErrOut, "\r\033[K  %s %s %d/%d  %s",
			dim("scanning"), dim(label), p.Done, p.Total, name)
		painted = true
	}
	return func(p catalog.ScanProgress) {
			now := time.Now()
			// The last file is always painted, so the count never stops short
			// of the total on a scan fast enough to finish inside one tick.
			if p.Done < p.Total && now.Sub(lastAt) < scanRedrawInterval {
				return
			}
			lastAt = now
			paint(p)
		}, func() {
			if painted {
				fmt.Fprint(env.ErrOut, "\r\033[K")
				painted = false
			}
		}
}

// terminalWidth reports the width of the terminal progress is drawn on,
// falling back to the conventional 80 columns when stderr is not one or cannot
// be measured.
func terminalWidth() int {
	if w, _, err := term.GetSize(int(os.Stderr.Fd())); err == nil && w > 0 {
		return w
	}
	return 80
}

// scanLabel names the source root a progress report came from. Both platforms
// report through one callback, and "1200/1994" means nothing without saying
// which library it is counting through.
func scanLabel(env *Env, root string) string {
	switch root {
	case env.Config.Sources.PS2:
		return "PS2 sources"
	case env.Config.Sources.PS1:
		return "PS1 sources"
	}
	return filepath.Base(root)
}

// withScanProgress builds the scan options for a command that should show
// progress, together with the cleanup that clears the line.
func withScanProgress(env *Env) (app.ScanOptions, func()) {
	report, finish := scanPrinter(env)
	return app.ScanOptions{OnProgress: report}, finish
}
