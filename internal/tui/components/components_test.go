package components

import (
	"strings"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

// Much of what this package lays out is already styled, so the fitting helpers
// have to measure display cells rather than bytes. Counting escape sequences
// as visible width silently eats most of a line.
func TestTruncateIsANSIAware(t *testing.T) {
	plain := "install selected"
	styled := lipgloss.NewStyle().Foreground(lipgloss.Color("42")).Render(plain)

	if got := Width(styled); got != len(plain) {
		t.Fatalf("Width of a styled string = %d, want %d", got, len(plain))
	}
	// A styled string that fits must come back untouched.
	if got := Truncate(styled, len(plain)); got != styled {
		t.Errorf("a fitting styled string was altered")
	}
	// Truncating must cut visible characters, not escape bytes.
	got := Truncate(styled, 8)
	if w := Width(got); w != 8 {
		t.Errorf("Truncate to 8 gave width %d (%q)", w, got)
	}
	if !strings.Contains(got, "…") {
		t.Errorf("Truncate did not mark the cut: %q", got)
	}
	if !strings.Contains(got, "instal") {
		t.Errorf("Truncate lost the visible text: %q", got)
	}
}

func TestTruncatePlain(t *testing.T) {
	cases := map[string]string{
		"short":       "short",
		"exactly ten": "exactly t…",
		"":            "",
	}
	for in, want := range cases {
		if got := Truncate(in, 10); got != want {
			t.Errorf("Truncate(%q, 10) = %q, want %q", in, got, want)
		}
	}
	if got := Truncate("abc", 0); got != "" {
		t.Errorf("Truncate to 0 = %q", got)
	}
	if got := Truncate("abc", 1); got != "…" {
		t.Errorf("Truncate to 1 = %q", got)
	}
}

func TestPadIsANSIAware(t *testing.T) {
	styled := lipgloss.NewStyle().Bold(true).Render("hi")
	got := Pad(styled, 6)
	if w := Width(got); w != 6 {
		t.Errorf("Pad width = %d, want 6 (%q)", w, got)
	}
	if !strings.HasSuffix(got, "    ") {
		t.Errorf("Pad did not append spaces: %q", got)
	}
}

func TestFooterKeepsEveryHintThatFits(t *testing.T) {
	hints := []Hint{
		{"↑↓", "move"}, {"space", "select"}, {"i", "install"},
		{"tab", "view"}, {"q", "quit"},
	}
	out := Footer(120, hints)
	for _, h := range hints {
		if !strings.Contains(out, h.Label) {
			t.Errorf("footer dropped %q at width 120:\n%q", h.Label, out)
		}
	}
	if w := Width(out); w > 120 {
		t.Errorf("footer is %d cells wide, over the 120 given", w)
	}
}

func TestTableRendersWithinWidth(t *testing.T) {
	tbl := NewTable([]Column{
		{Title: "SYS", Width: 3},
		{Title: "GAME", Flex: 3},
		{Title: "SIZE", Width: 10, Right: true},
	})
	tbl.Selectable = true
	tbl.SetSize(60, 6)
	tbl.SetRows([]Row{
		{Key: "a", Cells: []string{"PS2", "A Game With A Rather Long Title Indeed", "3.4 GiB"}},
		{Key: "b", Cells: []string{"PS1", "Short", "550 MiB"}},
	})
	for _, line := range strings.Split(tbl.View(), "\n") {
		if w := Width(line); w > 60 {
			t.Errorf("line is %d cells wide, over 60: %q", w, line)
		}
	}
}

func TestTableSelection(t *testing.T) {
	tbl := NewTable([]Column{{Title: "GAME", Flex: 1}})
	tbl.Selectable = true
	tbl.SetSize(40, 8)
	tbl.SetRows([]Row{
		{Key: "a", Cells: []string{"A"}},
		{Key: "b", Cells: []string{"B"}},
		{Key: "c", Cells: []string{"C"}},
	})

	// With nothing explicitly selected the cursor row stands in, so "press i
	// to install" works without a prior space.
	if got := tbl.Selected(); len(got) != 1 || got[0] != "a" {
		t.Errorf("implicit selection = %v, want [a]", got)
	}
	if tbl.ExplicitSelectionCount() != 0 {
		t.Error("cursor row counted as an explicit selection")
	}

	tbl.ToggleSelection()
	tbl.SetCursor(2)
	tbl.ToggleSelection()
	got := tbl.Selected()
	if len(got) != 2 || got[0] != "a" || got[1] != "c" {
		t.Errorf("selection = %v, want [a c] in row order", got)
	}
	if tbl.ExplicitSelectionCount() != 2 {
		t.Errorf("ExplicitSelectionCount = %d", tbl.ExplicitSelectionCount())
	}
	tbl.ClearSelection()
	if tbl.ExplicitSelectionCount() != 0 {
		t.Error("ClearSelection left a selection")
	}
}

// A background rescan replaces the rows; the cursor must stay on the same
// game rather than jumping.
func TestTableKeepsCursorAcrossRefresh(t *testing.T) {
	tbl := NewTable([]Column{{Title: "GAME", Flex: 1}})
	tbl.SetSize(40, 8)
	tbl.SetRows([]Row{
		{Key: "a", Cells: []string{"A"}},
		{Key: "b", Cells: []string{"B"}},
		{Key: "c", Cells: []string{"C"}},
	})
	tbl.SetCursor(2)

	// A new game appears at the front; the cursor should still be on "c".
	tbl.SetRows([]Row{
		{Key: "z", Cells: []string{"Z"}},
		{Key: "a", Cells: []string{"A"}},
		{Key: "b", Cells: []string{"B"}},
		{Key: "c", Cells: []string{"C"}},
	})
	if r, _ := tbl.CursorRow(); r.Key != "c" {
		t.Errorf("cursor moved to %q after a refresh, want c", r.Key)
	}
}

func TestTableEmpty(t *testing.T) {
	tbl := NewTable([]Column{{Title: "GAME", Flex: 1}})
	tbl.SetSize(40, 8)
	tbl.SetRows(nil)
	if tbl.Cursor() != -1 {
		t.Errorf("Cursor on an empty table = %d, want -1", tbl.Cursor())
	}
	if _, ok := tbl.CursorRow(); ok {
		t.Error("CursorRow reported a row on an empty table")
	}
	if got := tbl.Selected(); len(got) != 0 {
		t.Errorf("Selected on an empty table = %v", got)
	}
	if !strings.Contains(tbl.View(), "nothing to show") {
		t.Errorf("empty table view = %q", tbl.View())
	}
}

// A progress bar must never be drawn for work with no known extent; the
// indeterminate bar is what those stages get.
func TestProgressBars(t *testing.T) {
	if w := Width(Bar(20, 0.5)); w != 20 {
		t.Errorf("Bar width = %d", w)
	}
	if w := Width(Bar(20, 2.0)); w != 20 {
		t.Errorf("Bar clamps over-range fractions: width = %d", w)
	}
	if got := Percent(-1); strings.Contains(got, "0%") {
		t.Errorf("Percent(-1) = %q; an unknown fraction must not read as 0%%", got)
	}
	if got := Percent(0.42); strings.TrimSpace(got) != "42%" {
		t.Errorf("Percent(0.42) = %q", got)
	}
	for tick := 0; tick < 12; tick++ {
		if w := Width(IndeterminateBar(20, tick)); w != 20 {
			t.Errorf("IndeterminateBar at tick %d has width %d", tick, w)
		}
	}
}

func TestDialogDefaultsToCancel(t *testing.T) {
	// A stray Enter must never delete a game, so a danger dialog opens with
	// the cancel button highlighted.
	d := NewDanger("Remove", "gone forever", nil, "remove", nil)
	if d.Confirmed() {
		t.Error("a danger dialog opened on the confirm button")
	}
	d.Toggle()
	if !d.Confirmed() {
		t.Error("Toggle did not move to the confirm button")
	}
	if !d.HasChoice() {
		t.Error("a danger dialog should offer a choice")
	}
	if NewError("x", "y").HasChoice() {
		t.Error("an error dialog should have no confirm action")
	}
}

// Every row must start at the same column whether or not it is selected or
// under the cursor, so the columns stay aligned as the user moves around.
func TestTableRowsAlign(t *testing.T) {
	tbl := NewTable([]Column{{Title: "GAME", Width: 10}, {Title: "ID", Width: 8}})
	tbl.Selectable = true
	tbl.SetSize(40, 8)
	tbl.SetRows([]Row{
		{Key: "a", Cells: []string{"A", "1"}},
		{Key: "b", Cells: []string{"B", "2"}},
		{Key: "c", Cells: []string{"C", "3"}},
	})
	tbl.SetCursor(1)
	tbl.ToggleSelection() // selects row b, the cursor row, then advances
	tbl.SetCursor(0)

	lines := strings.Split(tbl.View(), "\n")[1:] // skip the header
	widths := map[int]int{}
	for i, line := range lines {
		widths[i] = Width(line)
	}
	for i := 1; i < len(lines); i++ {
		if widths[i] != widths[0] {
			t.Fatalf("row %d is %d cells wide, row 0 is %d:\n%q\n%q",
				i, widths[i], widths[0], lines[i], lines[0])
		}
	}
}

// A dialog must never be wider than the terminal, whatever it is asked to
// show: a device path is long enough to push an unconstrained box off screen.
func TestDialogFitsTerminal(t *testing.T) {
	long := "/dev/disk/by-id/ata-WDC_WD1200JB-00REA0_WD-WCANM1234567890123456789012345"
	d := NewDanger("Remove from the HDD", "This cannot be undone.", [][2]string{
		{"Title", "A Rather Long Game Title Indeed, With Punctuation"},
		{"Device", long},
	}, "remove", nil)

	for _, width := range []int{40, 60, 80, 110, 200} {
		out := d.View(width, 24)
		for _, line := range strings.Split(out, "\n") {
			if w := Width(line); w > width {
				t.Errorf("at width %d a dialog line is %d cells: %q", width, w, line)
			}
		}
	}
}

// A dialog must fit the terminal vertically too, or a long body pushes the
// buttons off the top of the screen where nobody can see them.
func TestDialogFitsShortTerminal(t *testing.T) {
	body := strings.Repeat("a line of help text\n", 40)
	d := NewInfo("Keys", body)
	for _, h := range []int{12, 20, 24, 40} {
		out := d.View(100, h)
		if got := strings.Count(out, "\n") + 1; got > h {
			t.Errorf("at height %d the dialog rendered %d lines", h, got)
		}
	}
}
