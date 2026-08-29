package tui

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/demo"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

func TestMain(m *testing.M) {
	logging.Discard()
	os.Exit(m.Run())
}

// newTestModel builds a model wired to a synthetic HDD and source library, so
// the interface can be driven end to end without hardware or external tools.
func newTestModel(t *testing.T) *Model {
	t.Helper()
	root := t.TempDir()
	// Keep the test out of the developer's real XDG directories.
	t.Setenv("XDG_CACHE_HOME", filepath.Join(root, "cache"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(root, "config"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(root, "state"))
	t.Setenv("XDG_RUNTIME_DIR", "")

	env, err := demo.Setup(filepath.Join(root, "demo"))
	if err != nil {
		t.Fatalf("build the demo environment: %v", err)
	}
	cfg := env.Config(config.Default())
	cfg.SetPath(filepath.Join(root, "config", "ps2hdd", "config.toml"))
	svc := app.New(cfg, env.Runner())
	t.Cleanup(func() { _ = svc.Close(context.Background()) })

	m := NewModel(context.Background(), svc, cfg)
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

// loadAll runs the model's data-loading commands synchronously and feeds the
// results back, which is what Init does asynchronously in a real session.
func loadAll(t *testing.T, m *Model) {
	t.Helper()
	for _, cmd := range []tea.Cmd{m.loadCatalog(), m.loadDrive(), m.loadAssets()} {
		if cmd == nil {
			continue
		}
		m.Update(cmd())
	}
}

func TestModelLoadsDemoLibrary(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)

	if m.catalogErr != nil {
		t.Fatalf("catalog error: %v", m.catalogErr)
	}
	if len(m.catalog.Entries) == 0 {
		t.Fatal("the catalog is empty; the synthetic library should not be")
	}
	installed, available, _ := m.catalog.Counts()
	if installed == 0 {
		t.Error("no installed games were read from the synthetic HDD")
	}
	if available == 0 {
		t.Error("no source games were read from the synthetic source directories")
	}
	if m.driveErr != nil {
		t.Fatalf("drive error: %v", m.driveErr)
	}
	if !m.driveStatus.APADetected {
		t.Error("the synthetic HDD was not recognised as APA")
	}
	// The demo deliberately omits the two Sony runtime files, so PS1 support
	// must read as not ready.
	if m.ps1Ready.Ready() {
		t.Error("PS1 reported ready despite the POPS runtime being absent")
	}
}

// Every view, at every plausible terminal size, must render inside the width
// it was given. Long device paths and long game titles are what break this.
func TestEveryViewFitsItsTerminal(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)

	sizes := []struct{ w, h int }{
		{80, 24}, {100, 30}, {120, 40}, {200, 50}, {56, 20},
	}
	for _, size := range sizes {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})
		for v := View(0); v < numViews; v++ {
			m.active = v
			out := m.View()
			for i, line := range strings.Split(out, "\n") {
				if got := components.Width(line); got > size.w {
					t.Errorf("%dx%d %s: line %d is %d cells wide:\n%q",
						size.w, size.h, v.Name(), i, got, line)
				}
			}
		}
	}
}

// The details pane and the dialogs are drawn instead of a view, so they need
// the same guarantee.
func TestOverlaysFitTheirTerminal(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)

	for _, size := range []struct{ w, h int }{{80, 24}, {100, 30}, {60, 20}} {
		m.Update(tea.WindowSizeMsg{Width: size.w, Height: size.h})

		m.active = ViewInstalled
		m.instTable.SetCursor(0)
		if e, ok := m.entryForCursor(m.instTable); ok {
			m.detail = &e
			checkWidth(t, m.View(), size.w, "details")
			m.detail = nil
		}

		m.active = ViewInstalled
		if _, _ = m.confirmRemove(m.instTable); m.dialog == nil {
			t.Fatal("confirmRemove produced no dialog")
		}
		checkWidth(t, m.View(), size.w, "remove dialog")
		m.dialog = nil

		m.active = ViewPS2Sources
		m.ps2Table.SetCursor(0)
		m.ps2Table.SelectAll()
		if _, _ = m.confirmInstall(m.ps2Table); m.dialog != nil {
			checkWidth(t, m.View(), size.w, "install dialog")
			m.dialog = nil
		}
		m.ps2Table.ClearSelection()
	}
}

func checkWidth(t *testing.T, out string, width int, what string) {
	t.Helper()
	for i, line := range strings.Split(out, "\n") {
		if got := components.Width(line); got > width {
			t.Errorf("%s at width %d: line %d is %d cells:\n%q", what, width, i, got, line)
		}
	}
}

// Removing a game must always show what is about to go before it goes, with
// the cancel button selected.
func TestRemoveDialogNamesTheGame(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	m.active = ViewInstalled
	m.instTable.SetCursor(0)

	entry, ok := m.entryForCursor(m.instTable)
	if !ok {
		t.Fatal("no installed game to remove")
	}
	m.confirmRemove(m.instTable)
	if m.dialog == nil {
		t.Fatal("no confirmation dialog")
	}
	if m.dialog.Confirmed() {
		t.Error("the remove dialog opened on the confirm button")
	}
	fields := map[string]bool{}
	for _, kv := range m.dialog.Details {
		fields[kv[0]] = true
		if kv[0] == "Title" && kv[1] != entry.Title {
			t.Errorf("dialog title = %q, want %q", kv[1], entry.Title)
		}
	}
	// The plan requires title, platform, game ID and size before confirmation.
	for _, want := range []string{"Title", "Platform", "Game ID", "Size"} {
		if !fields[want] {
			t.Errorf("the remove dialog does not show %q: %+v", want, m.dialog.Details)
		}
	}
}

// Pressing esc on a dialog must cancel, never proceed.
func TestDialogEscapeCancels(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	m.active = ViewInstalled
	m.confirmRemove(m.instTable)
	if m.dialog == nil {
		t.Fatal("no dialog")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.dialog != nil {
		t.Error("esc did not dismiss the dialog")
	}
}

// Enter on a danger dialog with the default (cancel) button selected must not
// run the action.
func TestDangerDialogEnterDefaultsToCancel(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	m.active = ViewInstalled
	m.confirmRemove(m.instTable)
	if m.dialog == nil {
		t.Fatal("no dialog")
	}
	before := len(m.catalog.Entries)
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.dialog != nil {
		t.Fatal("enter left the dialog open")
	}
	loadAll(t, m)
	if got := len(m.catalog.Entries); got != before {
		t.Errorf("the library changed from %d to %d entries; enter on the default button removed something",
			before, got)
	}
}

func TestViewNavigation(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)

	m.active = ViewPS2Sources
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if m.active != ViewPS1Sources {
		t.Errorf("tab went to %v", m.active)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.active != ViewPS2Sources {
		t.Errorf("shift-tab went to %v", m.active)
	}
	// The number keys jump straight to a view.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'6'}})
	if m.active != ViewDrive {
		t.Errorf("6 went to %v, want Drive", m.active)
	}
	// Cycling all the way round returns to the start.
	start := m.active
	for i := 0; i < int(numViews); i++ {
		m.Update(tea.KeyMsg{Type: tea.KeyTab})
	}
	if m.active != start {
		t.Errorf("cycling every view ended at %v, want %v", m.active, start)
	}
}

func TestSearchFiltersTheList(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	m.active = ViewInstalled
	before := len(m.instTable.Rows)
	if before == 0 {
		t.Fatal("no installed rows to filter")
	}

	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
	if !m.searching {
		t.Fatal("/ did not start a search")
	}
	for _, r := range "burnout" {
		m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if got := len(m.instTable.Rows); got == 0 || got >= before {
		t.Errorf("searching for burnout gave %d rows, was %d", got, before)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.searching || m.search != "" {
		t.Error("esc did not end the search")
	}
	if got := len(m.instTable.Rows); got != before {
		t.Errorf("clearing the search left %d rows, want %d", got, before)
	}
}

// A source view must never claim something is installed on the strength of it
// being in a source directory.
func TestSourceViewDoesNotInferInstalled(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	for _, e := range m.catalog.Entries {
		if e.Installed && e.PartitionName == "" && e.StorageBackend == "" {
			t.Errorf("%s is marked installed with no on-HDD record", e.Title)
		}
	}
}

func TestQueueViewEmptyState(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	m.active = ViewQueue
	out := m.View()
	if !strings.Contains(out, "empty") {
		t.Errorf("the empty queue does not say so:\n%s", out)
	}
	if !strings.Contains(out, "space") {
		t.Errorf("the empty queue does not say how to fill it:\n%s", out)
	}
}

func TestHelpDialogOpens(t *testing.T) {
	m := newTestModel(t)
	loadAll(t, m)
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'?'}})
	if m.dialog == nil {
		t.Fatal("? did not open the help")
	}
	if !strings.Contains(m.dialog.Body, "install") {
		t.Error("the help does not mention installing")
	}
}
