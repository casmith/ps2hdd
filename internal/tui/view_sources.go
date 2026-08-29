package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

// sourceColumns is the layout of the two source browsers.
func sourceColumns() []components.Column {
	return []components.Column{
		{Title: "GAME", Flex: 3},
		{Title: "ID", Width: 12},
		{Title: "SIZE", Width: 10, Right: true},
		{Title: "DISCS", Width: 5, Right: true},
		{Title: "STATUS", Width: 10},
	}
}

// refreshTables rebuilds every table from the current catalog and filters.
func (m *Model) refreshTables() {
	m.ps2Table.SetRows(m.sourceRows(model.PlatformPS2))
	m.ps1Table.SetRows(m.sourceRows(model.PlatformPS1))
	m.instTable.SetRows(m.installedRows())
	m.refreshAssetTable()
}

// sourceRows lists the titles available in a platform's source directory.
func (m *Model) sourceRows(p model.Platform) []components.Row {
	f := catalog.Filter{Platform: p, Search: m.search}
	var rows []components.Row
	for _, e := range m.catalog.Apply(f) {
		if !e.AvailableInSource {
			continue
		}
		discs := ""
		if e.IsMultiDisc() {
			discs = fmt.Sprintf("%d", e.DiscCount())
		}
		status := "Available"
		style := &components.StyleBase
		if e.Installed {
			status = "Installed"
			// Something already on the HDD is dimmed rather than hidden: the
			// user still wants to see it in the browser.
			style = &components.StyleMuted
		}
		rows = append(rows, components.Row{
			Key:   e.Key(),
			Style: style,
			Cells: []string{e.Title, e.GameID, model.HumanSize(e.SizeBytes), discs, status},
		})
	}
	return rows
}

func (m *Model) handleSourcesKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.activeTable()
	if t.Update(msg) {
		return m, nil
	}
	switch {
	case keyMatches(msg, "enter"):
		if e, ok := m.entryForCursor(t); ok {
			m.detail = &e
		}
	case keyMatches(msg, "a"):
		t.SelectAll()
	case keyMatches(msg, "c"):
		t.ClearSelection()
	case keyMatches(msg, "i"):
		return m.confirmInstall(t)
	}
	return m, nil
}

// confirmInstall builds the install confirmation for the current selection.
func (m *Model) confirmInstall(t *components.Table) (tea.Model, tea.Cmd) {
	games := m.selectedGames(t, false)
	if len(games) == 0 {
		return m, m.statusFor("Nothing selected.", true)
	}

	var installable []model.Game
	var skipped int
	for _, g := range games {
		if m.isInstalled(g) {
			skipped++
			continue
		}
		installable = append(installable, g)
	}
	if len(installable) == 0 {
		return m, m.statusFor("Already installed.", true)
	}

	var total int64
	for _, g := range installable {
		total += g.SizeBytes
	}
	details := [][2]string{
		{"Games", fmt.Sprintf("%d", len(installable))},
		{"Total size", model.HumanSize(total)},
		{"Free on HDD", model.HumanSize(m.driveStatus.FreeBytes)},
		{"Device", m.svc.Config.Device},
	}
	if skipped > 0 {
		details = append(details, [2]string{"Skipping", fmt.Sprintf("%d already installed", skipped)})
	}
	body := installList(installable)
	if total > m.driveStatus.FreeBytes && m.driveStatus.FreeBytes > 0 {
		body += "\n\nThis is more than the free space on the HDD; later items will fail."
	}
	m.dialog = components.NewConfirm("Install", body, details, "install", installable)
	return m, nil
}

func installList(games []model.Game) string {
	const maxShown = 8
	var b strings.Builder
	for i, g := range games {
		if i == maxShown {
			fmt.Fprintf(&b, "\n… and %d more", len(games)-maxShown)
			break
		}
		if i > 0 {
			b.WriteByte('\n')
		}
		fmt.Fprintf(&b, "%s  %s (%s)", g.Platform.Label(), g.Title, model.HumanSize(g.SizeBytes))
	}
	return b.String()
}

// selectedGames maps the table's selection back to catalog entries.
//
// When installedOnly is set, only titles on the HDD are returned, which is
// what the remove and artwork actions need.
func (m *Model) selectedGames(t *components.Table, installedOnly bool) []model.Game {
	if t == nil {
		return nil
	}
	keys := map[string]bool{}
	for _, k := range t.Selected() {
		keys[k] = true
	}
	var out []model.Game
	for _, e := range m.catalog.Entries {
		if !keys[e.Key()] {
			continue
		}
		if installedOnly {
			if !e.Installed {
				continue
			}
			out = append(out, e.Game)
			continue
		}
		// For an install, the source-side record is the one that carries the
		// image path and the media type.
		if e.SourceGame != nil {
			out = append(out, *e.SourceGame)
			continue
		}
		out = append(out, e.Game)
	}
	return out
}

func (m *Model) entryForCursor(t *components.Table) (catalog.CatalogEntry, bool) {
	r, ok := t.CursorRow()
	if !ok {
		return catalog.CatalogEntry{}, false
	}
	for _, e := range m.catalog.Entries {
		if e.Key() == r.Key {
			return e, true
		}
	}
	return catalog.CatalogEntry{}, false
}

func (m *Model) isInstalled(g model.Game) bool {
	for _, e := range m.catalog.Entries {
		if e.Key() == g.Key() {
			return e.Installed
		}
	}
	return false
}

// renderSources draws a source browser.
func (m *Model) renderSources(p model.Platform) string {
	t := m.ps2Table
	dir := m.svc.Config.Sources.PS2
	if p == model.PlatformPS1 {
		t = m.ps1Table
		dir = m.svc.Config.Sources.PS1
	}

	var b strings.Builder
	title := fmt.Sprintf("%s source", p.Label())
	if dir == "" {
		b.WriteString(components.StyleHeader.Render(title) + "  " +
			components.StyleWarning.Render("no directory configured"))
		b.WriteString("\n\n" + components.StyleMuted.Render(
			"Set one in Settings, or run:\n  ps2hdd config set sources."+string(p)+" /path/to/games"))
		return b.String()
	}
	// The path is truncated rather than wrapped: a wrapped header pushes the
	// table down and makes the layout jump as the window is resized.
	b.WriteString(components.StyleHeader.Render(title) + "  " +
		components.StyleMuted.Render(components.Truncate(dir, m.contentWidth()-len(title)-6)))
	if m.loadingCat {
		b.WriteString("  " + components.Spinner(m.tickCount))
	}
	b.WriteString("\n\n")
	b.WriteString(t.View())

	var total int64
	selected := t.ExplicitSelectionCount()
	for _, g := range m.selectedGames(t, false) {
		total += g.SizeBytes
	}
	b.WriteString("\n\n")
	if selected > 0 {
		b.WriteString(components.StyleAccent.Render(fmt.Sprintf("Selected: %d", selected)) +
			components.StyleMuted.Render(fmt.Sprintf("   Estimated install size: %s", model.HumanSize(total))))
	} else if info := t.ScrollInfo(); info != "" {
		b.WriteString(components.StyleMuted.Render(info))
	}
	return b.String()
}
