package tui

import (
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// Messages carrying the results of background work. Every long operation is a
// tea.Cmd that returns one of these, so the update loop is never blocked.

// catalogLoadedMsg carries a completed library refresh.
type catalogLoadedMsg struct {
	catalog  catalog.Catalog
	warnings []error
	err      error
}

// driveStatusMsg carries a completed drive read.
type driveStatusMsg struct {
	status model.DriveStatus
	ready  ps1.Readiness
	err    error
}

// assetStatusMsg carries a completed artwork inventory.
type assetStatusMsg struct {
	rows []asset.StatusRow
	err  error
}

// assetSyncProgressMsg reports one artwork file finished.
type assetSyncProgressMsg struct {
	done, total int
	item        asset.PlanItem
}

// assetSyncDoneMsg carries a finished artwork sync.
type assetSyncDoneMsg struct {
	plan   asset.Plan
	result asset.Result
	err    error
}

// queueUpdateMsg carries a queue item state change.
type queueUpdateMsg struct{ item app.QueueItem }

// removeDoneMsg carries a finished removal.
type removeDoneMsg struct {
	report app.RemoveReport
	err    error
}

// setupDoneMsg carries a finished PS1 setup check.
type setupDoneMsg struct {
	report app.SetupPS1Report
	err    error
}

// configSavedMsg reports a settings write.
type configSavedMsg struct{ err error }

// statusMsg puts a transient line in the status bar.
type statusMsg struct {
	text    string
	isError bool
}

// clearStatusMsg wipes the status line after a delay.
type clearStatusMsg struct{ id int }

// tickMsg drives spinners and indeterminate bars.
type tickMsg time.Time

// tickInterval is slow enough not to burn power on an idle screen and fast
// enough that a spinner reads as motion.
const tickInterval = 120 * time.Millisecond

func tick() tea.Cmd {
	return tea.Tick(tickInterval, func(t time.Time) tea.Msg { return tickMsg(t) })
}

// statusFor returns a command that shows a message and clears it later.
func (m *Model) statusFor(text string, isError bool) tea.Cmd {
	m.statusID++
	id := m.statusID
	m.status = text
	m.statusIsError = isError
	return tea.Tick(6*time.Second, func(time.Time) tea.Msg { return clearStatusMsg{id: id} })
}
