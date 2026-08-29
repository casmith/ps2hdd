// Package tui implements the ps2hdd terminal interface.
//
// The interface is a Bubble Tea program over the same internal/app services
// the CLI uses. No disk logic lives here: an Update handler dispatches a
// tea.Cmd that calls a service on a background goroutine and delivers the
// result as a message, which is what keeps the interface responsive while a
// scan, a download or an install is running.
package tui

import tea "github.com/charmbracelet/bubbletea"

// View identifies a screen.
type View int

const (
	ViewPS2Sources View = iota
	ViewPS1Sources
	ViewInstalled
	ViewAssets
	ViewQueue
	ViewDrive
	ViewSettings
	numViews
)

// viewNames are the sidebar labels, in order.
var viewNames = [numViews]string{
	"PS2 Games",
	"PS1 Games",
	"Installed",
	"Assets",
	"Queue",
	"Drive",
	"Settings",
}

// Name returns the sidebar label.
func (v View) Name() string {
	if v < 0 || v >= numViews {
		return "?"
	}
	return viewNames[v]
}

// Next and Prev cycle through the views, which is what tab and shift-tab do.
func (v View) Next() View { return View((int(v) + 1) % int(numViews)) }
func (v View) Prev() View { return View((int(v) - 1 + int(numViews)) % int(numViews)) }

// keyMatches reports whether a key message is one of the given key strings.
func keyMatches(msg tea.KeyMsg, keys ...string) bool {
	s := msg.String()
	for _, k := range keys {
		if s == k {
			return true
		}
	}
	return false
}
