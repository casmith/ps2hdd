package components

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Header renders the top bar: the program name on the left, the drive summary
// on the right.
func Header(width int, left, right string) string {
	style := lipgloss.NewStyle().Background(ColorHeaderBg).Foreground(ColorText).Width(width)
	l := StyleBold.Render(left)
	r := right
	gap := width - Width(l) - Width(r) - 2
	if gap < 1 {
		gap = 1
		r = Truncate(right, max(0, width-Width(l)-3))
	}
	return style.Render(" " + l + strings.Repeat(" ", gap) + r + " ")
}

// Footer renders the key hints.
func Footer(width int, hints []Hint) string {
	var parts []string
	for _, h := range hints {
		parts = append(parts, StyleAccent.Render(h.Key)+" "+StyleMuted.Render(h.Label))
	}
	line := strings.Join(parts, StyleMuted.Render("  ·  "))
	return lipgloss.NewStyle().Width(width).Render(" " + Truncate(line, max(0, width-2)))
}

// Hint is one key binding shown in the footer.
type Hint struct {
	Key   string
	Label string
}

// StatusLine renders a single-line message with a severity colour.
func StatusLine(width int, kind DialogKind, msg string) string {
	if msg == "" {
		return lipgloss.NewStyle().Width(width).Render("")
	}
	style := StyleMuted
	switch kind {
	case DialogError, DialogDanger:
		style = StyleDanger
	case DialogInfo:
		style = StyleSuccess
	}
	return lipgloss.NewStyle().Width(width).Render(" " + style.Render(Truncate(msg, max(0, width-2))))
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}
