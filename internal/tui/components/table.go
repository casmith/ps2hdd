package components

import (
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Column describes one table column.
type Column struct {
	Title string
	// Width is the fixed width in cells. A column with Flex set instead takes
	// a share of whatever space the fixed columns leave.
	Width int
	Flex  int
	// Right right-aligns the cell contents, which is what sizes want.
	Right bool
}

// Row is one table row. Cells are pre-rendered strings; Style, when set, is
// applied to the whole row unless the row is the cursor row.
type Row struct {
	Cells []string
	// Key identifies the row across refreshes so the cursor and the selection
	// survive a rescan.
	Key string
	// Style colours the row, e.g. amber for a title missing artwork.
	Style *lipgloss.Style
}

// Table is a scrollable list with a cursor and an optional multi-selection.
//
// It is deliberately not bubbles/table: that widget does not offer
// multi-selection, and the selection is the whole point of the Sources views.
type Table struct {
	Columns []Column
	Rows    []Row

	// Selectable turns on the space-bar multi-selection.
	Selectable bool

	cursor   int
	offset   int
	width    int
	height   int
	selected map[string]bool
}

// NewTable creates a table.
func NewTable(cols []Column) *Table {
	return &Table{Columns: cols, selected: map[string]bool{}}
}

// SetSize sets the drawing area. Height is the total lines available including
// the header row.
func (t *Table) SetSize(w, h int) {
	t.width, t.height = w, h
	t.clampCursor()
}

// SetRows replaces the rows, keeping the cursor on the same key where possible
// so a background rescan does not move the user's place.
func (t *Table) SetRows(rows []Row) {
	var currentKey string
	if t.cursor >= 0 && t.cursor < len(t.Rows) {
		currentKey = t.Rows[t.cursor].Key
	}
	t.Rows = rows
	if currentKey != "" {
		for i, r := range rows {
			if r.Key == currentKey {
				t.cursor = i
				t.clampCursor()
				return
			}
		}
	}
	t.clampCursor()
}

// Cursor returns the highlighted row index, or -1 when the table is empty.
func (t *Table) Cursor() int {
	if len(t.Rows) == 0 {
		return -1
	}
	return t.cursor
}

// CursorRow returns the highlighted row.
func (t *Table) CursorRow() (Row, bool) {
	if len(t.Rows) == 0 || t.cursor >= len(t.Rows) {
		return Row{}, false
	}
	return t.Rows[t.cursor], true
}

// SetCursor moves the cursor to an index.
func (t *Table) SetCursor(i int) {
	t.cursor = i
	t.clampCursor()
}

// Selected returns the keys of the selected rows, in row order. When nothing
// is explicitly selected the cursor row is returned, which is what makes
// "press i to install" work without a prior space-bar press.
func (t *Table) Selected() []string {
	var out []string
	for _, r := range t.Rows {
		if t.selected[r.Key] {
			out = append(out, r.Key)
		}
	}
	if len(out) == 0 {
		if r, ok := t.CursorRow(); ok {
			return []string{r.Key}
		}
	}
	return out
}

// ExplicitSelectionCount reports how many rows were selected with space.
func (t *Table) ExplicitSelectionCount() int {
	n := 0
	for _, r := range t.Rows {
		if t.selected[r.Key] {
			n++
		}
	}
	return n
}

// IsSelected reports whether a key is selected.
func (t *Table) IsSelected(key string) bool { return t.selected[key] }

// ToggleSelection flips the cursor row's selection.
func (t *Table) ToggleSelection() {
	r, ok := t.CursorRow()
	if !ok || !t.Selectable {
		return
	}
	if t.selected[r.Key] {
		delete(t.selected, r.Key)
	} else {
		t.selected[r.Key] = true
	}
}

// SelectAll selects every visible row.
func (t *Table) SelectAll() {
	if !t.Selectable {
		return
	}
	for _, r := range t.Rows {
		t.selected[r.Key] = true
	}
}

// ClearSelection drops the multi-selection.
func (t *Table) ClearSelection() { t.selected = map[string]bool{} }

// Update handles navigation keys. It returns true when the key was consumed,
// so a view can fall through to its own bindings.
func (t *Table) Update(msg tea.KeyMsg) bool {
	switch msg.String() {
	case "up", "k":
		t.cursor--
	case "down", "j":
		t.cursor++
	case "pgup":
		t.cursor -= t.pageSize()
	case "pgdown":
		t.cursor += t.pageSize()
	case "home", "g":
		t.cursor = 0
	case "end", "G":
		t.cursor = len(t.Rows) - 1
	case " ":
		t.ToggleSelection()
		// Advancing after a toggle makes selecting a run of games one keypress
		// per game rather than two.
		t.cursor++
	default:
		return false
	}
	t.clampCursor()
	return true
}

func (t *Table) pageSize() int {
	n := t.height - 1
	if n < 1 {
		return 1
	}
	return n
}

func (t *Table) clampCursor() {
	if len(t.Rows) == 0 {
		t.cursor, t.offset = 0, 0
		return
	}
	if t.cursor < 0 {
		t.cursor = 0
	}
	if t.cursor >= len(t.Rows) {
		t.cursor = len(t.Rows) - 1
	}
	visible := t.pageSize()
	if t.cursor < t.offset {
		t.offset = t.cursor
	}
	if t.cursor >= t.offset+visible {
		t.offset = t.cursor - visible + 1
	}
	if t.offset < 0 {
		t.offset = 0
	}
}

// widths resolves the column widths for the current table width.
func (t *Table) widths() []int {
	out := make([]int, len(t.Columns))
	fixed, flexTotal := 0, 0
	gaps := 0
	if len(t.Columns) > 1 {
		gaps = len(t.Columns) - 1
	}
	for i, c := range t.Columns {
		if c.Flex > 0 {
			flexTotal += c.Flex
			continue
		}
		out[i] = c.Width
		fixed += c.Width
	}
	// Two extra cells at the front carry the selection marker.
	avail := t.width - fixed - gaps - 2
	if avail < 0 {
		avail = 0
	}
	for i, c := range t.Columns {
		if c.Flex == 0 {
			continue
		}
		w := avail * c.Flex / flexTotal
		if w < 4 {
			w = 4
		}
		out[i] = w
	}
	return out
}

// View renders the table.
func (t *Table) View() string {
	if t.width <= 0 || t.height <= 0 {
		return ""
	}
	widths := t.widths()
	var b strings.Builder

	// Header.
	b.WriteString("  ")
	for i, c := range t.Columns {
		b.WriteString(StyleHeader.Render(align(c.Title, widths[i], c.Right)))
		if i < len(t.Columns)-1 {
			b.WriteByte(' ')
		}
	}
	b.WriteByte('\n')

	if len(t.Rows) == 0 {
		b.WriteString(StyleMuted.Render("  (nothing to show)"))
		return b.String()
	}

	visible := t.pageSize()
	end := t.offset + visible
	if end > len(t.Rows) {
		end = len(t.Rows)
	}
	for i := t.offset; i < end; i++ {
		r := t.Rows[i]
		// The marker is always exactly two display cells so every row starts
		// at the same column, selected or not.
		marker := "  "
		if t.Selectable {
			if t.selected[r.Key] {
				marker = StyleAccent.Render("●") + " "
			} else {
				marker = "○ "
			}
		}

		var line strings.Builder
		for j, c := range t.Columns {
			cell := ""
			if j < len(r.Cells) {
				cell = r.Cells[j]
			}
			line.WriteString(align(Truncate(cell, widths[j]), widths[j], c.Right))
			if j < len(t.Columns)-1 {
				line.WriteByte(' ')
			}
		}

		text := line.String()
		switch {
		case i == t.cursor:
			// The whole row, marker included, carries the cursor highlight.
			b.WriteString(StyleSelected.Render(marker + text))
		case r.Style != nil:
			b.WriteString(marker + r.Style.Render(text))
		default:
			b.WriteString(marker + text)
		}
		if i < end-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// ScrollInfo renders a "12/340" position indicator, or "" when everything fits.
func (t *Table) ScrollInfo() string {
	if len(t.Rows) == 0 {
		return ""
	}
	return strings.TrimSpace(itoa(t.cursor+1) + "/" + itoa(len(t.Rows)))
}

// align fits s into w display cells, left- or right-aligned. Like Truncate and
// Pad it is ANSI-aware, because table cells may already carry colour.
func align(s string, w int, right bool) string {
	if !right {
		return Pad(s, w)
	}
	cur := Width(s)
	if cur >= w {
		return Truncate(s, w)
	}
	return strings.Repeat(" ", w-cur) + s
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
