package components

import (
	"fmt"
	"strings"
)

// Bar renders a determinate progress bar.
//
// A caller that has no percentage must use Spinner instead: showing a made-up
// bar is worse than showing motion with no number.
func Bar(width int, fraction float64) string {
	if width < 4 {
		width = 4
	}
	if fraction < 0 {
		fraction = 0
	}
	if fraction > 1 {
		fraction = 1
	}
	filled := int(float64(width) * fraction)
	if filled > width {
		filled = width
	}
	return StyleAccent.Render(strings.Repeat("█", filled)) +
		StyleMuted.Render(strings.Repeat("░", width-filled))
}

// Percent renders a percentage label.
func Percent(fraction float64) string {
	if fraction < 0 {
		return "  --%"
	}
	return fmt.Sprintf("%4.0f%%", fraction*100)
}

// spinnerFrames is a plain-ASCII spinner, so it renders on a terminal with no
// Unicode support and over a serial console.
var spinnerFrames = []string{"|", "/", "-", "\\"}

// Spinner returns the frame for a tick counter.
func Spinner(tick int) string {
	return StyleAccent.Render(spinnerFrames[tick%len(spinnerFrames)])
}

// IndeterminateBar renders a bar with a moving block, for work whose extent is
// unknown.
func IndeterminateBar(width, tick int) string {
	if width < 6 {
		width = 6
	}
	const blockLen = 4
	span := width - blockLen
	if span < 1 {
		span = 1
	}
	// Bounce rather than wrap, so the motion reads as "working" rather than
	// as progress that keeps restarting.
	pos := tick % (2 * span)
	if pos >= span {
		pos = 2*span - pos
	}
	return StyleMuted.Render(strings.Repeat("░", pos)) +
		StyleAccent.Render(strings.Repeat("█", blockLen)) +
		StyleMuted.Render(strings.Repeat("░", width-blockLen-pos))
}
