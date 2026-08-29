// Package components holds the reusable widgets the ps2hdd views are built
// from: a selectable table, a modal dialog, a progress bar and the header and
// footer bars.
//
// Widgets here hold presentation state only. Everything they display is passed
// in by a view, which in turn gets it from internal/app; no component reaches
// into a service or the disk.
package components

import (
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Palette is the colour set the whole interface uses. Colours are given as
// adaptive pairs so the interface stays legible on light and dark terminals,
// and every one degrades to plain text on a terminal without colour.
var (
	ColorText     = lipgloss.AdaptiveColor{Light: "236", Dark: "252"}
	ColorMuted    = lipgloss.AdaptiveColor{Light: "244", Dark: "245"}
	ColorAccent   = lipgloss.AdaptiveColor{Light: "27", Dark: "39"}
	ColorSuccess  = lipgloss.AdaptiveColor{Light: "28", Dark: "42"}
	ColorWarning  = lipgloss.AdaptiveColor{Light: "130", Dark: "214"}
	ColorDanger   = lipgloss.AdaptiveColor{Light: "160", Dark: "203"}
	ColorSelectBg = lipgloss.AdaptiveColor{Light: "254", Dark: "238"}
	ColorBorder   = lipgloss.AdaptiveColor{Light: "250", Dark: "240"}
	ColorHeaderBg = lipgloss.AdaptiveColor{Light: "254", Dark: "236"}
)

// Shared styles.
var (
	StyleBase     = lipgloss.NewStyle().Foreground(ColorText)
	StyleMuted    = lipgloss.NewStyle().Foreground(ColorMuted)
	StyleAccent   = lipgloss.NewStyle().Foreground(ColorAccent)
	StyleSuccess  = lipgloss.NewStyle().Foreground(ColorSuccess)
	StyleWarning  = lipgloss.NewStyle().Foreground(ColorWarning)
	StyleDanger   = lipgloss.NewStyle().Foreground(ColorDanger)
	StyleBold     = lipgloss.NewStyle().Bold(true)
	StyleHeader   = lipgloss.NewStyle().Bold(true).Foreground(ColorText)
	StyleSelected = lipgloss.NewStyle().Background(ColorSelectBg).Foreground(ColorText).Bold(true)
	StyleBorder   = lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(ColorBorder)
)

// Width reports the display width of a string, ignoring ANSI escape sequences
// and accounting for wide runes.
func Width(s string) int { return ansi.StringWidth(s) }

// Truncate shortens s to n display cells, appending an ellipsis when it had to
// cut.
//
// It is ANSI-aware. That matters because much of what this package renders is
// already styled by the time it is laid out, and counting escape bytes as
// display cells silently eats most of a styled line: a footer of coloured key
// hints would show two of them and drop the rest.
func Truncate(s string, n int) string {
	if n <= 0 {
		return ""
	}
	if ansi.StringWidth(s) <= n {
		return s
	}
	if n == 1 {
		return "…"
	}
	return ansi.Truncate(s, n-1, "") + "…"
}

// Pad right-pads s to n display cells, truncating if it is already wider.
func Pad(s string, n int) string {
	w := ansi.StringWidth(s)
	if w >= n {
		return ansi.Truncate(s, n, "")
	}
	return s + spaces(n-w)
}

// spaces returns n spaces.
func spaces(n int) string {
	if n <= 0 {
		return ""
	}
	const blanks = "                                                                "
	if n <= len(blanks) {
		return blanks[:n]
	}
	out := make([]byte, n)
	for i := range out {
		out[i] = ' '
	}
	return string(out)
}
