package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
)

// Every function here returns a tea.Cmd: work happens on a goroutine Bubble
// Tea owns, and the result comes back as a message. Nothing in this file may
// be called from a View, and nothing in it blocks the update loop.

func (m *Model) loadCatalog() tea.Cmd {
	svc, ctx := m.svc, m.ctx
	return func() tea.Msg {
		c, warnings, err := svc.Catalog(ctx)
		return catalogLoadedMsg{catalog: c, warnings: warnings, err: err}
	}
}

func (m *Model) loadDrive() tea.Cmd {
	svc, ctx := m.svc, m.ctx
	return func() tea.Msg {
		st, err := svc.Status(ctx)
		if err != nil {
			return driveStatusMsg{err: err}
		}
		ready, _ := svc.PS1Readiness(ctx)
		return driveStatusMsg{status: st, ready: ready}
	}
}

func (m *Model) loadAssets() tea.Cmd {
	svc, ctx := m.svc, m.ctx
	return func() tea.Msg {
		rows, err := svc.AssetStatus(ctx, nil)
		return assetStatusMsg{rows: rows, err: err}
	}
}

// enqueue adds games to the install queue and starts it if it is idle.
func (m *Model) enqueue(games []model.Game) tea.Cmd {
	if len(games) == 0 {
		return nil
	}
	m.queue.Add(games...)
	m.active = ViewQueue

	// Queue updates arrive on a goroutine, so they are funnelled through a
	// channel that a tea.Cmd drains one message at a time.
	if m.queueEvents == nil {
		m.queueEvents = make(chan app.QueueItem, 64)
		m.queue.OnUpdate(func(it app.QueueItem) {
			select {
			case m.queueEvents <- it:
			default:
				// A full channel means the interface is behind; dropping an
				// intermediate progress update is harmless because the next
				// one carries the current state.
			}
		})
	}
	m.queue.Start(m.ctx)
	return tea.Batch(
		m.waitForQueueEvent(),
		func() tea.Msg {
			return statusMsg{text: pluralGames(len(games)) + " queued."}
		},
	)
}

// waitForQueueEvent blocks on the queue channel and re-arms itself, which is
// the standard Bubble Tea pattern for turning a channel into messages.
func (m *Model) waitForQueueEvent() tea.Cmd {
	ch := m.queueEvents
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		it, ok := <-ch
		if !ok {
			return nil
		}
		return queueUpdateMsg{item: it}
	}
}

func (m *Model) removeGames(games []model.Game) tea.Cmd {
	svc, ctx := m.svc, m.ctx
	cmds := make([]tea.Cmd, 0, len(games))
	for _, g := range games {
		g := g
		cmds = append(cmds, func() tea.Msg {
			rep, err := svc.Remove(ctx, g, app.RemoveOptions{})
			return removeDoneMsg{report: rep, err: err}
		})
	}
	return tea.Sequence(cmds...)
}

func (m *Model) syncAssets(games []model.Game) tea.Cmd {
	svc, ctx := m.svc, m.ctx
	m.syncing = true
	m.syncDone, m.syncTotal = 0, 0

	if m.assetEvents == nil {
		m.assetEvents = make(chan assetSyncProgressMsg, 64)
	}
	ch := m.assetEvents
	return tea.Batch(
		m.waitForAssetEvent(),
		func() tea.Msg {
			plan, res, err := svc.SyncAssets(ctx, games, app.SyncAssetsOptions{
				OnProgress: func(done, total int, item asset.PlanItem) {
					select {
					case ch <- assetSyncProgressMsg{done: done, total: total, item: item}:
					default:
					}
				},
			})
			return assetSyncDoneMsg{plan: plan, result: res, err: err}
		},
	)
}

func (m *Model) waitForAssetEvent() tea.Cmd {
	ch := m.assetEvents
	if ch == nil {
		return nil
	}
	return func() tea.Msg {
		msg, ok := <-ch
		if !ok {
			return nil
		}
		return msg
	}
}

func (m *Model) saveConfig(cfg config.Config) tea.Cmd {
	svc := m.svc
	return func() tea.Msg {
		if err := cfg.Validate(); err != nil {
			return configSavedMsg{err: err}
		}
		if err := cfg.Save(); err != nil {
			return configSavedMsg{err: err}
		}
		// The running services keep using the configuration they were built
		// with for anything already in flight; applying it here means the next
		// scan and the next sync see the new values.
		svc.Config = cfg
		return configSavedMsg{}
	}
}

func (m *Model) checkPS1Setup() tea.Cmd {
	svc, ctx := m.svc, m.ctx
	return func() tea.Msg {
		rep, err := svc.SetupPS1(ctx, app.SetupPS1Options{})
		return setupDoneMsg{report: rep, err: err}
	}
}

func pluralGames(n int) string {
	if n == 1 {
		return "1 game"
	}
	return itoa(n) + " games"
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	return string(buf[i:])
}
