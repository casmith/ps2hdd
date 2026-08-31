package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

func (m *Model) handleQueueKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyMatches(msg, "c"):
		if m.queue.Pending() == 0 {
			return m, m.statusFor("Nothing to cancel.", false)
		}
		m.dialog = components.NewDanger(
			"Cancel the queue",
			"The item being written is stopped where it is. A partially written game stays on the HDD; "+
				"ps2hdd will not try to unwind it. Remove it from the Installed view afterwards.",
			[][2]string{{"Queued", fmt.Sprintf("%d", m.queue.Pending())}},
			"cancel-queue", nil)
	case keyMatches(msg, "x"):
		m.queue.Clear()
	case keyMatches(msg, "R"):
		n := 0
		for _, it := range m.queue.Items() {
			if m.queue.Retry(it.ID) {
				n++
			}
		}
		if n == 0 {
			return m, m.statusFor("Nothing to retry.", false)
		}
		m.queue.Start(m.ctx)
		return m, tea.Batch(m.waitForQueueEvent(), m.statusFor(fmt.Sprintf("Retrying %d item(s).", n), false))
	}
	return m, nil
}

func (m *Model) renderQueue() string {
	items := m.queue.Items()
	var b strings.Builder
	b.WriteString(components.StyleHeader.Render("Install queue"))
	complete, failed, pending := m.queue.Summary()
	b.WriteString("  " + components.StyleMuted.Render(
		fmt.Sprintf("%d complete · %d failed · %d pending", complete, failed, pending)))
	b.WriteString("\n\n")

	if len(items) == 0 {
		b.WriteString(components.StyleMuted.Render(m.wrap(
			"The queue is empty.\n\nSelect games in the PS2 or PS1 source view with space, then press i.")))
		return b.String()
	}

	barWidth := m.contentWidth() - 26
	if barWidth < 10 {
		barWidth = 10
	}
	// Only as many items as fit are drawn; the counts above still cover the rest.
	maxItems := (m.contentHeight() - 4) / 3
	if maxItems < 1 {
		maxItems = 1
	}
	shown := items
	if len(shown) > maxItems {
		shown = shown[:maxItems]
	}

	for i, it := range shown {
		if i > 0 {
			b.WriteString("\n\n")
		}
		fmt.Fprintf(&b, "%d. %s %s\n   ", it.ID,
			components.StyleBold.Render(components.Truncate(it.Game.Title, m.contentWidth()-24)),
			components.StyleMuted.Render(model.HumanSize(it.Game.SizeBytes)))

		switch it.State {
		case app.QueueWaiting:
			b.WriteString(components.StyleMuted.Render(strings.Repeat("░", barWidth) + "  Waiting"))
		case app.QueueComplete:
			b.WriteString(components.Bar(barWidth, 1) + "  " + components.StyleSuccess.Render("Complete"))
			for _, w := range it.Warnings {
				b.WriteString("\n   " + components.StyleWarning.Render("! ") +
					components.StyleMuted.Render(components.Truncate(firstLine(w), m.contentWidth()-8)))
			}
		case app.QueueFailed:
			b.WriteString(components.StyleDanger.Render("Failed") + "  " +
				components.StyleMuted.Render(components.Truncate(firstLine(it.Err), m.contentWidth()-12)))
		case app.QueueCancelled:
			b.WriteString(components.StyleMuted.Render("Cancelled"))
		default:
			if it.Progress < 0 {
				b.WriteString(components.IndeterminateBar(barWidth, m.tickCount) + "  " + it.StatusText)
			} else {
				b.WriteString(components.Bar(barWidth, it.Progress) + " " +
					components.Percent(it.Progress) + "  " + it.StatusText)
			}
		}
	}
	if len(items) > len(shown) {
		fmt.Fprintf(&b, "\n\n%s", components.StyleMuted.Render(
			fmt.Sprintf("… and %d more", len(items)-len(shown))))
	}
	return b.String()
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
