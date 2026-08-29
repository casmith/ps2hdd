package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

// assetColumns builds a column per enabled asset type, so the table shows the
// slots the user actually cares about rather than all nine.
func assetColumns(want []model.AssetType) []components.Column {
	cols := []components.Column{
		{Title: "SYS", Width: 3},
		{Title: "GAME", Flex: 3},
	}
	for _, t := range want {
		w := len(string(t))
		if w < 4 {
			w = 4
		}
		cols = append(cols, components.Column{Title: string(t), Width: w})
	}
	return cols
}

func (m *Model) refreshAssetTable() {
	want := m.svc.Config.WantedAssets()
	m.artTable.Columns = assetColumns(want)

	var rows []components.Row
	for _, r := range m.assetRows {
		missing := 0
		cells := []string{r.Game.Platform.Label(), r.Game.Title}
		for _, t := range want {
			if r.Present[t] {
				cells = append(cells, components.StyleSuccess.Render("yes"))
			} else {
				cells = append(cells, components.StyleMuted.Render("no"))
				missing++
			}
		}
		if m.assetsMissingOnly && missing == 0 {
			continue
		}
		if m.search != "" && !matchesSearch(r.Game, m.search) {
			continue
		}
		style := &components.StyleBase
		if missing > 0 {
			style = &components.StyleWarning
		}
		rows = append(rows, components.Row{Key: r.Game.Key(), Style: style, Cells: cells})
	}
	m.artTable.SetRows(rows)
}

func matchesSearch(g model.Game, needle string) bool {
	n := strings.ToLower(needle)
	return strings.Contains(strings.ToLower(g.Title), n) ||
		strings.Contains(strings.ToLower(g.GameID), n) ||
		strings.Contains(model.NormalizeGameID(g.GameID), model.NormalizeGameID(needle))
}

func (m *Model) handleAssetsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	t := m.artTable
	if t.Update(msg) {
		return m, nil
	}
	switch {
	case keyMatches(msg, "f"):
		m.assetsMissingOnly = !m.assetsMissingOnly
		m.refreshAssetTable()
	case keyMatches(msg, "c"):
		t.ClearSelection()
	case keyMatches(msg, "a"):
		games := m.assetGamesForSelection()
		if len(games) == 0 {
			return m, m.statusFor("Nothing selected.", true)
		}
		return m, m.syncAssets(games)
	case keyMatches(msg, "A"):
		var games []model.Game
		for _, r := range m.assetRows {
			if len(r.Missing) > 0 {
				games = append(games, r.Game)
			}
		}
		if len(games) == 0 {
			return m, m.statusFor("Nothing is missing.", false)
		}
		m.dialog = components.NewConfirm(
			"Sync all missing artwork",
			fmt.Sprintf("Fetch every missing slot for %s.\n\nExisting files are never replaced.", pluralGames(len(games))),
			[][2]string{{"Provider", m.providerName()}},
			"sync-assets", games)
	}
	return m, nil
}

func (m *Model) providerName() string {
	p, err := m.svc.AssetProvider()
	if err != nil {
		return "misconfigured"
	}
	return p.Name()
}

func (m *Model) assetGamesForSelection() []model.Game {
	keys := map[string]bool{}
	for _, k := range m.artTable.Selected() {
		keys[k] = true
	}
	var out []model.Game
	for _, r := range m.assetRows {
		if keys[r.Game.Key()] {
			out = append(out, r.Game)
		}
	}
	return out
}

func (m *Model) renderAssets() string {
	var b strings.Builder
	b.WriteString(components.StyleHeader.Render("Artwork"))
	b.WriteString("  " + components.StyleMuted.Render("provider: "+m.providerName()))
	if m.assetsMissingOnly {
		b.WriteString("  " + components.StyleAccent.Render("incomplete only"))
	}
	if m.loadingArt {
		b.WriteString("  " + components.Spinner(m.tickCount))
	}
	b.WriteString("\n\n")

	if m.assetErr != nil {
		b.WriteString(components.StyleDanger.Render(m.assetErr.Error()))
		b.WriteString("\n\n" + components.StyleMuted.Render(
			"Artwork lives in +OPL/ART, which needs pfsfuse to reach."))
		return b.String()
	}
	if m.loadingArt && len(m.assetRows) == 0 {
		b.WriteString(components.StyleMuted.Render("Reading +OPL/ART…"))
		return b.String()
	}
	b.WriteString(m.artTable.View())

	b.WriteString("\n\n")
	if m.syncing {
		w := m.contentWidth() - 20
		if m.syncTotal > 0 {
			frac := float64(m.syncDone) / float64(m.syncTotal)
			b.WriteString(fmt.Sprintf("%s %s  %d/%d",
				components.Bar(w, frac), components.Percent(frac), m.syncDone, m.syncTotal))
		} else {
			b.WriteString(components.IndeterminateBar(w, m.tickCount) + "  fetching…")
		}
		return b.String()
	}

	complete, incomplete := 0, 0
	for _, r := range m.assetRows {
		if len(r.Missing) == 0 {
			complete++
		} else {
			incomplete++
		}
	}
	line := fmt.Sprintf("%d complete", complete)
	if incomplete > 0 {
		line += "   " + components.StyleWarning.Render(fmt.Sprintf("%d incomplete", incomplete))
	}
	if n := m.artTable.ExplicitSelectionCount(); n > 0 {
		line = components.StyleAccent.Render(fmt.Sprintf("Selected: %d", n)) + "   " + line
	}
	b.WriteString(components.StyleMuted.Render(line))
	return b.String()
}
