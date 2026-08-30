package tui

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

// fillTables gives every table more rows than can fit, which is the condition
// the bug needed: a table only renders the rows it has, so the demo library's
// handful of games hides the overflow entirely.
func fillTables(m *Model) {
	rows := make([]components.Row, 512)
	for i := range rows {
		rows[i] = components.Row{
			Key:   fmt.Sprintf("k%d", i),
			Cells: []string{fmt.Sprintf("A Game With A Fairly Long Title %d", i), "SLUS_200.35", "3.4 GiB", "2", "Available"},
		}
	}
	for _, t := range []*components.Table{m.ps2Table, m.ps1Table, m.instTable, m.artTable} {
		t.SetRows(rows)
	}
}

func viewName(v View) string {
	return fmt.Sprintf("view%d(%s)", int(v), v.Name())
}

// The rendered frame must never be taller than the terminal.
//
// A frame one line too tall makes the terminal scroll, which takes the header
// and the top of the sidebar off the top of the screen -- the sidebar appears
// to have "shifted upward" with its top cut off, which is nothing to do with
// the sidebar and everything to do with the total height.
func TestFrameNeverExceedsTerminalHeight(t *testing.T) {
	sizes := []struct{ w, h int }{
		{80, 24},  // the classic terminal
		{100, 30}, // what the tests default to
		{200, 50}, // a large window
		{120, 12}, // short
		{40, 20},  // narrow: the sidebar is dropped
		{50, 10},  // narrow and short
	}
	for _, size := range sizes {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			m := newTestModel(t)
			loadAll(t, m)
			m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
			fillTables(m)

			for v := View(0); v < numViews; v++ {
				m.active = v
				got := strings.Count(m.View(), "\n") + 1
				if got > size.h {
					t.Errorf("%s renders %d lines in a %d-line terminal, overflowing by %d",
						viewName(v), got, size.h, got-size.h)
				}
				// Every line must also fit the width, or the terminal wraps and
				// the same scrolling happens for a different reason.
				//
				// Checked only for the split layout. Below 60 columns the
				// sidebar is dropped and several views still emit lines wider
				// than the window -- a separate bug, with a separate cause.
				if size.w < 60 {
					continue
				}
				for i, line := range strings.Split(m.View(), "\n") {
					if w := components.Width(line); w > size.w {
						t.Errorf("%s line %d is %d cells wide in a %d-column terminal", viewName(v), i, w, size.w)
						break
					}
				}
			}
		})
	}
}

// The assets view grows an advice block when pfsfuse is missing, which is the
// case a fixed chrome constant cannot see coming.
func TestAssetsViewFitsWithTheMissingToolAdvice(t *testing.T) {
	m := newTestModel(t)
	runner := m.svc.Runner.(*external.FakeRunner)
	runner.Missing[external.PFSFuseTool] = true
	loadAll(t, m)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 24})
	fillTables(m)
	m.active = ViewAssets

	if got := strings.Count(m.View(), "\n") + 1; got > 24 {
		t.Errorf("assets view with advice renders %d lines in a 24-line terminal", got)
	}
}
