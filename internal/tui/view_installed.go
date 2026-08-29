package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

func installedColumns() []components.Column {
	return []components.Column{
		{Title: "SYS", Width: 3},
		{Title: "GAME", Flex: 3},
		{Title: "ID", Width: 12},
		{Title: "SIZE", Width: 10, Right: true},
		{Title: "DISCS", Width: 5, Right: true},
		{Title: "ARTWORK", Width: 12},
	}
}

// installedFilters cycles through the filter presets bound to `f`.
var installedFilters = []struct {
	label  string
	filter catalog.Filter
}{
	{"all", catalog.Filter{Installed: true}},
	{"PS2", catalog.Filter{Installed: true, Platform: model.PlatformPS2}},
	{"PS1", catalog.Filter{Installed: true, Platform: model.PlatformPS1}},
	{"missing artwork", catalog.Filter{Installed: true, MissingAsset: true}},
	{"multi-disc", catalog.Filter{Installed: true, MultiDisc: true}},
}

func (m *Model) installedRows() []components.Row {
	f := m.installedFilter
	f.Installed = true
	f.Search = m.search
	var rows []components.Row
	for _, e := range m.catalog.Apply(f) {
		discs := ""
		if e.IsMultiDisc() {
			discs = fmt.Sprintf("%d", e.DiscCount())
		}
		art := "complete"
		style := &components.StyleBase
		if n := len(e.MissingAssets); n > 0 {
			art = fmt.Sprintf("%d missing", n)
			style = &components.StyleWarning
		}
		rows = append(rows, components.Row{
			Key:   e.Key(),
			Style: style,
			Cells: []string{e.Platform.Label(), e.Title, e.GameID,
				model.HumanSize(e.SizeBytes), discs, art},
		})
	}
	return rows
}

func (m *Model) handleInstalledKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.instTable
	if t.Update(msg) {
		return m, nil
	}
	switch {
	case keyMatches(msg, "enter"):
		if e, ok := m.entryForCursor(t); ok {
			m.detail = &e
		}
	case keyMatches(msg, "a"):
		games := m.selectedGames(t, true)
		if len(games) == 0 {
			return m, m.statusFor("Nothing selected.", true)
		}
		return m, m.syncAssets(games)
	case keyMatches(msg, "c"):
		t.ClearSelection()
	case keyMatches(msg, "f"):
		m.cycleInstalledFilter()
	case keyMatches(msg, "d"):
		return m.confirmRemove(t)
	}
	return m, nil
}

func (m *Model) cycleInstalledFilter() {
	current := m.installedFilterIndex()
	next := installedFilters[(current+1)%len(installedFilters)]
	m.installedFilter = next.filter
	m.refreshTables()
}

func (m *Model) installedFilterIndex() int {
	for i, f := range installedFilters {
		if f.filter.Platform == m.installedFilter.Platform &&
			f.filter.MissingAsset == m.installedFilter.MissingAsset &&
			f.filter.MultiDisc == m.installedFilter.MultiDisc {
			return i
		}
	}
	return 0
}

// confirmRemove shows exactly what will be deleted before deleting it. The
// dialog defaults to cancel, and the details spell out title, platform, ID and
// size, as required before any destructive action.
func (m *Model) confirmRemove(t *components.Table) (tea.Model, tea.Cmd) {
	games := m.selectedGames(t, true)
	if len(games) == 0 {
		return m, m.statusFor("Nothing selected.", true)
	}
	var total int64
	for _, g := range games {
		total += g.SizeBytes
	}

	var details [][2]string
	if len(games) == 1 {
		g := games[0]
		details = [][2]string{
			{"Title", g.Title},
			{"Platform", platformName(g.Platform)},
			{"Game ID", g.GameID},
			{"Size", model.HumanSize(g.SizeBytes)},
		}
		if g.IsMultiDisc() {
			details = append(details, [2]string{"Discs", fmt.Sprintf("%d (all removed)", g.DiscCount())})
		}
		if g.PartitionName != "" {
			details = append(details, [2]string{"On HDD", g.PartitionName})
		}
	} else {
		details = [][2]string{
			{"Games", fmt.Sprintf("%d", len(games))},
			{"Total size", model.HumanSize(total)},
		}
	}
	details = append(details,
		[2]string{"Device", m.svc.Config.Device},
		[2]string{"Artwork", "kept"})

	body := "This cannot be undone."
	if len(games) > 1 {
		body = installList(games) + "\n\nThis cannot be undone."
	}
	m.dialog = components.NewDanger("Remove from the HDD", body, details, "remove", games)
	return m, nil
}

func (m *Model) renderInstalled() string {
	var b strings.Builder
	b.WriteString(components.StyleHeader.Render("Installed"))
	b.WriteString("  " + components.StyleMuted.Render("filter: "+installedFilters[m.installedFilterIndex()].label))
	if m.loadingCat {
		b.WriteString("  " + components.Spinner(m.tickCount))
	}
	b.WriteString("\n\n")

	if m.catalogErr != nil {
		b.WriteString(components.StyleDanger.Render(m.catalogErr.Error()))
		return b.String()
	}
	b.WriteString(m.instTable.View())

	b.WriteString("\n\n")
	inst, _, missing := m.catalog.Counts()
	line := fmt.Sprintf("%d installed", inst)
	if missing > 0 {
		line += fmt.Sprintf("   %s", components.StyleWarning.Render(fmt.Sprintf("%d missing artwork", missing)))
	}
	if n := m.instTable.ExplicitSelectionCount(); n > 0 {
		line = components.StyleAccent.Render(fmt.Sprintf("Selected: %d", n)) + "   " + line
	}
	b.WriteString(components.StyleMuted.Render(line))
	return b.String()
}

// renderDetail draws the game details pane.
func (m *Model) renderDetail(e catalog.CatalogEntry) string {
	var b strings.Builder
	b.WriteString(components.StyleHeader.Render(e.Title))
	b.WriteString("\n\n")

	rows := [][2]string{
		{"Platform", platformName(e.Platform)},
		{"ID", e.GameID},
		{"Size", model.HumanSize(e.SizeBytes)},
		{"Installed", yesNo(e.Installed)},
	}
	if e.Media != model.MediaUnknown {
		rows = append(rows, [2]string{"Media", strings.ToUpper(string(e.Media))})
	}
	if e.StorageBackend != "" {
		rows = append(rows, [2]string{"Stored as", storageName(e.StorageBackend)})
	}
	if e.PartitionName != "" {
		rows = append(rows, [2]string{"On HDD", e.PartitionName})
	}
	if e.SourcePath != "" {
		rows = append(rows, [2]string{"Source", e.SourcePath})
	}
	b.WriteString(renderKV(rows, m.contentWidth()))

	if len(e.Discs) > 1 {
		b.WriteString("\n\n" + components.StyleHeader.Render(fmt.Sprintf("Discs (%d)", len(e.Discs))) + "\n")
		for _, d := range e.Discs {
			loc := d.SourcePath
			if d.InstalledName != "" {
				loc = d.InstalledName
			}
			b.WriteString(fmt.Sprintf("\n  %-6s %-14s %-10s %s",
				fmt.Sprintf("Disc %d", d.Number), d.GameID,
				model.HumanSize(d.SizeBytes),
				components.StyleMuted.Render(components.Truncate(loc, m.contentWidth()-36))))
		}
	}

	if e.Installed {
		b.WriteString("\n\n" + components.StyleHeader.Render("Artwork") + "\n")
		if len(e.MissingAssets) == 0 {
			b.WriteString("\n  " + components.StyleSuccess.Render("complete"))
		} else {
			for _, t := range e.MissingAssets {
				dim := ""
				if d, ok := model.Dimensions(e.Platform, t); ok {
					dim = fmt.Sprintf(" (%dx%d)", d.Width, d.Height)
				}
				b.WriteString(fmt.Sprintf("\n  %-6s %s%s", t,
					components.StyleWarning.Render("missing"), components.StyleMuted.Render(dim)))
			}
		}
	}
	b.WriteString("\n\n" + components.StyleMuted.Render("esc to go back"))
	return b.String()
}

// renderKV lays out an aligned key/value block, truncating values to maxWidth
// so a long device path never wraps and pushes the rest of the view down.
func renderKV(rows [][2]string, maxWidth int) string {
	keyWidth := 0
	for _, r := range rows {
		if len(r[0]) > keyWidth {
			keyWidth = len(r[0])
		}
	}
	valueWidth := maxWidth - keyWidth - 4
	if valueWidth < 8 {
		valueWidth = 8
	}
	var b strings.Builder
	for i, r := range rows {
		if i > 0 {
			b.WriteByte('\n')
		}
		b.WriteString("  " + components.StyleMuted.Render(components.Pad(r[0], keyWidth)) +
			"  " + components.Truncate(r[1], valueWidth))
	}
	return b.String()
}

func platformName(p model.Platform) string {
	switch p {
	case model.PlatformPS1:
		return "PlayStation 1"
	case model.PlatformPS2:
		return "PlayStation 2"
	}
	return string(p)
}

func storageName(b string) string {
	switch b {
	case model.BackendHDL:
		return "HDLoader partition"
	case model.BackendPOPS:
		return "POPS virtual CD (VCD)"
	}
	return b
}

func yesNo(b bool) string {
	if b {
		return components.StyleSuccess.Render("yes")
	}
	return "no"
}
