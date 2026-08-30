package cli

import (
	"bufio"
	"fmt"
	"io"
	"strings"
	"text/tabwriter"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

// colorEnabled controls ANSI output. It is switched off by --no-color and
// whenever output is not a terminal, so piping ps2hdd into a file or a script
// gives clean text.
var colorEnabled = true

func disableColor() { colorEnabled = false }

// SetColor lets main disable colour when stdout is not a terminal.
func SetColor(on bool) { colorEnabled = on }

const (
	ansiReset = "\x1b[0m"
	ansiBold  = "\x1b[1m"
	ansiDim   = "\x1b[2m"
	ansiRed   = "\x1b[31m"
	ansiGreen = "\x1b[32m"
	ansiAmber = "\x1b[33m"
)

func colorize(code, s string) string {
	if !colorEnabled || s == "" {
		return s
	}
	return code + s + ansiReset
}

func bold(s string) string  { return colorize(ansiBold, s) }
func dim(s string) string   { return colorize(ansiDim, s) }
func green(s string) string { return colorize(ansiGreen, s) }
func amber(s string) string { return colorize(ansiAmber, s) }
func red(s string) string   { return colorize(ansiRed, s) }

// newTable returns a tabwriter configured the way every listing in the CLI
// uses it.
func newTable(w io.Writer) *tabwriter.Writer {
	return tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
}

// mark renders a present/absent tick. The words are spelled out rather than
// using a bare glyph so the output survives a terminal without the font.
func mark(ok bool) string {
	if ok {
		return green("yes")
	}
	return dim("no")
}

// renderGames writes the unified library table.
func renderGames(w io.Writer, entries []catalog.CatalogEntry, showAssets bool) {
	if len(entries) == 0 {
		fmt.Fprintln(w, "No games.")
		return
	}
	t := newTable(w)
	header := "SYSTEM\tGAME\tID\tSIZE\tDISCS\tSTATUS"
	if showAssets {
		header += "\tARTWORK"
	}
	fmt.Fprintln(t, bold(header))
	for _, e := range entries {
		discs := ""
		if e.IsMultiDisc() {
			discs = fmt.Sprintf("%d", e.DiscCount())
		}
		row := fmt.Sprintf("%s\t%s\t%s\t%s\t%s\t%s",
			e.Platform.Label(), truncate(e.Title, 44), e.GameID,
			model.HumanSize(e.SizeBytes), discs, statusLabel(e))
		if showAssets {
			row += "\t" + artworkLabel(e)
		}
		fmt.Fprintln(t, row)
	}
	t.Flush()
}

func statusLabel(e catalog.CatalogEntry) string {
	switch e.State() {
	case catalog.StateInstalledAndSource:
		return green("Installed") + dim(" + source")
	case catalog.StateInstalled:
		return green("Installed")
	default:
		return "Available"
	}
}

func artworkLabel(e catalog.CatalogEntry) string {
	if !e.Installed {
		return dim("-")
	}
	// Never claim completeness that was not verified: reading +OPL needs
	// pfsfuse, and without it the answer is genuinely unknown.
	if !e.AssetsKnown {
		return dim("unknown")
	}
	if len(e.MissingAssets) == 0 {
		return green("complete")
	}
	names := make([]string, 0, len(e.MissingAssets))
	for _, t := range e.MissingAssets {
		names = append(names, string(t))
	}
	return amber(fmt.Sprintf("%d missing", len(e.MissingAssets))) + dim(" ("+strings.Join(names, ",")+")")
}

// truncate shortens a string for a fixed-width column.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

// section prints a heading.
func section(w io.Writer, title string) {
	fmt.Fprintf(w, "\n%s\n", bold(title))
}

// kv prints an aligned key/value block.
func kv(w io.Writer, pairs [][2]string) {
	width := 0
	for _, p := range pairs {
		if len(p[0]) > width {
			width = len(p[0])
		}
	}
	for _, p := range pairs {
		fmt.Fprintf(w, "  %-*s  %s\n", width, p[0], p[1])
	}
}

// confirm asks a yes/no question. It returns false on EOF so a piped
// invocation without --yes declines rather than proceeding.
func confirm(in io.Reader, out io.Writer, prompt string) bool {
	fmt.Fprintf(out, "%s [y/N] ", prompt)
	r := bufio.NewReader(in)
	line, err := r.ReadString('\n')
	if err != nil && line == "" {
		fmt.Fprintln(out)
		return false
	}
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}
