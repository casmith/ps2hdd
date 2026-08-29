package tui

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"syscall"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

// sidebarWidth is the fixed width of the view list.
const sidebarWidth = 20

// Model is the whole interface's state.
//
// UI state (which view is active, where each cursor is, what is selected) is
// kept strictly separate from service state (the catalog, the drive status,
// the queue), so a background refresh replaces data without disturbing the
// user's place.
type Model struct {
	ctx context.Context
	svc *Services

	width, height int
	active        View
	quitting      bool
	tickCount     int

	// Service state.
	catalog      catalog.Catalog
	catalogErr   error
	warnings     []error
	loadingCat   bool
	driveStatus  model.DriveStatus
	driveErr     error
	ps1Ready     ps1.Readiness
	loadingDrive bool
	assetRows    []asset.StatusRow
	assetErr     error
	loadingArt   bool
	syncing      bool
	syncDone     int
	syncTotal    int

	queue *app.Queue

	// Per-view UI state.
	ps2Table  *components.Table
	ps1Table  *components.Table
	instTable *components.Table
	artTable  *components.Table
	settings  *settingsModel

	// searching and search hold the incremental filter state.
	searching bool
	search    string
	// filters holds the per-view filter toggles.
	installedFilter   catalog.Filter
	assetsMissingOnly bool

	// showPartitions expands the Drive view's partition table.
	showPartitions bool

	// detail is the entry shown in the details pane, if any.
	detail *catalog.CatalogEntry

	dialog *components.Dialog

	// queueEvents and assetEvents turn callbacks that fire on worker
	// goroutines into messages the update loop can consume.
	queueEvents chan app.QueueItem
	assetEvents chan assetSyncProgressMsg

	status        string
	statusIsError bool
	statusID      int
}

// Services is the subset of the service layer the interface needs. Keeping it
// as an interface lets the view tests drive the whole program against a
// synthetic HDD.
type Services = app.Services

// Run starts the interface. It is the function main hands to the CLI, so that
// `ps2hdd` with no arguments opens the interface and `ps2hdd <command>` does
// not.
func Run(env EnvAdapter) error {
	// A signal-cancelled context means SIGINT and SIGTERM tear the program
	// down through the same path a quit does, so the deferred mount release
	// below always runs.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	svc := env.Services()
	// Release every PFS mount the session created, whatever happened: a leaked
	// FUSE mount holds the HDD busy and confuses the next run. The CLI's
	// Teardown does this too; both are cheap and idempotent, and having it
	// here means the interface is safe to embed anywhere.
	defer func() { _ = svc.Close(context.Background()) }()

	m := NewModel(ctx, svc, env.ConfigValue())
	p := tea.NewProgram(m, tea.WithAltScreen(), tea.WithContext(ctx))
	_, err := p.Run()
	if errors.Is(err, tea.ErrProgramKilled) || errors.Is(err, context.Canceled) {
		// A signal is not a failure.
		return nil
	}
	return err
}

// NewModel builds the initial state.
func NewModel(ctx context.Context, svc *Services, cfg config.Config) *Model {
	m := &Model{
		ctx:    ctx,
		svc:    svc,
		active: ViewInstalled,
	}
	m.ps2Table = components.NewTable(sourceColumns())
	m.ps2Table.Selectable = true
	m.ps1Table = components.NewTable(sourceColumns())
	m.ps1Table.Selectable = true
	m.instTable = components.NewTable(installedColumns())
	m.instTable.Selectable = true
	m.artTable = components.NewTable(assetColumns(cfg.WantedAssets()))
	m.artTable.Selectable = true
	m.settings = newSettingsModel(cfg)

	m.queue = app.NewQueue(svc, app.InstallOptions{SyncAssets: cfg.Install.SyncAssets})
	return m
}

// Init starts the first loads.
func (m *Model) Init() tea.Cmd {
	m.loadingCat, m.loadingDrive = true, true
	return tea.Batch(m.loadCatalog(), m.loadDrive(), tick())
}

// Update handles every message.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		return m, nil

	case tickMsg:
		m.tickCount++
		return m, tick()

	case tea.KeyMsg:
		return m.handleKey(msg)

	case catalogLoadedMsg:
		m.loadingCat = false
		m.catalogErr = msg.err
		m.warnings = msg.warnings
		if msg.err == nil {
			m.catalog = msg.catalog
			m.refreshTables()
		}
		return m, nil

	case driveStatusMsg:
		m.loadingDrive = false
		m.driveErr = msg.err
		m.driveStatus = msg.status
		m.ps1Ready = msg.ready
		return m, nil

	case assetStatusMsg:
		m.loadingArt = false
		m.assetErr = msg.err
		m.assetRows = msg.rows
		m.refreshAssetTable()
		return m, nil

	case assetSyncProgressMsg:
		m.syncDone, m.syncTotal = msg.done, msg.total
		return m, nil

	case assetSyncDoneMsg:
		m.syncing = false
		if msg.err != nil {
			m.dialog = components.NewError("Artwork sync failed", msg.err.Error())
			return m, nil
		}
		text := fmt.Sprintf("Installed %d artwork file(s).", len(msg.result.Installed))
		if n := len(msg.plan.Unavailable); n > 0 {
			text += fmt.Sprintf(" %d slot(s) no provider has.", n)
		}
		return m, tea.Batch(m.statusFor(text, false), m.loadAssets(), m.loadCatalog())

	case queueUpdateMsg:
		// The queue keeps its own state; the message exists so the view
		// repaints, and so a finished install refreshes the library.
		if msg.item.State == app.QueueComplete {
			return m, tea.Batch(m.loadCatalog(), m.loadDrive())
		}
		return m, nil

	case removeDoneMsg:
		if msg.err != nil {
			m.dialog = components.NewError("Remove failed", msg.err.Error())
			return m, nil
		}
		return m, tea.Batch(
			m.statusFor(fmt.Sprintf("Removed %s.", msg.report.Game.Title), false),
			m.loadCatalog(), m.loadDrive())

	case setupDoneMsg:
		if msg.err != nil {
			m.dialog = components.NewError("PS1 setup", msg.err.Error())
			return m, nil
		}
		m.ps1Ready = msg.report.Readiness
		return m, nil

	case configSavedMsg:
		if msg.err != nil {
			m.dialog = components.NewError("Could not save the configuration", msg.err.Error())
			return m, nil
		}
		return m, tea.Batch(m.statusFor("Configuration saved.", false), m.loadCatalog())

	case statusMsg:
		return m, m.statusFor(msg.text, msg.isError)

	case clearStatusMsg:
		if msg.id == m.statusID {
			m.status = ""
			m.statusIsError = false
		}
		return m, nil
	}
	return m, nil
}

// handleKey routes a keypress: the dialog first, then the search bar, then
// global bindings, then the active view.
func (m *Model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dialog != nil {
		return m.handleDialogKey(msg)
	}
	if m.searching {
		return m.handleSearchKey(msg)
	}

	switch {
	case keyMatches(msg, "ctrl+c"):
		return m.quit()
	case keyMatches(msg, "q"):
		if m.queue.Pending() > 0 {
			m.dialog = components.NewConfirm(
				"Work in progress",
				fmt.Sprintf("%d item(s) are still queued. Quitting cancels them.", m.queue.Pending()),
				nil, "quit", nil)
			return m, nil
		}
		return m.quit()
	case keyMatches(msg, "?"):
		m.dialog = components.NewInfo("Keys", helpText())
		return m, nil
	case keyMatches(msg, "tab"):
		m.active = m.active.Next()
		return m, m.onViewChange()
	case keyMatches(msg, "shift+tab"):
		m.active = m.active.Prev()
		return m, m.onViewChange()
	case keyMatches(msg, "1", "2", "3", "4", "5", "6", "7"):
		n := int(msg.String()[0] - '1')
		if n < int(numViews) {
			m.active = View(n)
			return m, m.onViewChange()
		}
	case keyMatches(msg, "r"):
		return m, m.refreshAll()
	case keyMatches(msg, "/"):
		if m.activeTable() != nil {
			m.searching = true
			return m, nil
		}
	case keyMatches(msg, "esc"):
		if m.detail != nil {
			m.detail = nil
			return m, nil
		}
		if m.search != "" {
			m.search = ""
			m.refreshTables()
			return m, nil
		}
	}

	switch m.active {
	case ViewPS2Sources, ViewPS1Sources:
		return m.handleSourcesKey(msg)
	case ViewInstalled:
		return m.handleInstalledKey(msg)
	case ViewAssets:
		return m.handleAssetsKey(msg)
	case ViewQueue:
		return m.handleQueueKey(msg)
	case ViewDrive:
		return m.handleDriveKey(msg)
	case ViewSettings:
		return m.handleSettingsKey(msg)
	}
	return m, nil
}

func (m *Model) quit() (tea.Model, tea.Cmd) {
	m.quitting = true
	m.queue.Cancel()
	return m, tea.Quit
}

func (m *Model) handleDialogKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	d := m.dialog
	switch {
	case keyMatches(msg, "esc"), keyMatches(msg, "n"):
		m.dialog = nil
		return m, nil
	case keyMatches(msg, "left", "right", "tab", "h", "l"):
		d.Toggle()
		return m, nil
	case keyMatches(msg, "y"):
		if d.HasChoice() {
			d.SetYes(true)
			return m.runDialogAction()
		}
		m.dialog = nil
		return m, nil
	case keyMatches(msg, "enter", " "):
		if !d.HasChoice() {
			m.dialog = nil
			return m, nil
		}
		if !d.Confirmed() {
			m.dialog = nil
			return m, nil
		}
		return m.runDialogAction()
	}
	return m, nil
}

func (m *Model) runDialogAction() (tea.Model, tea.Cmd) {
	d := m.dialog
	m.dialog = nil
	switch d.OnConfirm {
	case "quit":
		return m.quit()
	case "remove":
		games, _ := d.Payload.([]model.Game)
		return m, m.removeGames(games)
	case "install":
		games, _ := d.Payload.([]model.Game)
		return m, m.enqueue(games)
	case "cancel-queue":
		m.queue.Cancel()
		return m, m.statusFor("Queue cancelled.", false)
	case "sync-assets":
		games, _ := d.Payload.([]model.Game)
		return m, m.syncAssets(games)
	case "save-config":
		cfg, _ := d.Payload.(config.Config)
		return m, m.saveConfig(cfg)
	}
	return m, nil
}

func (m *Model) handleSearchKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case keyMatches(msg, "enter"):
		m.searching = false
	case keyMatches(msg, "esc"):
		m.searching = false
		m.search = ""
		m.refreshTables()
	case keyMatches(msg, "backspace"):
		if m.search != "" {
			r := []rune(m.search)
			m.search = string(r[:len(r)-1])
			m.refreshTables()
		}
	default:
		if s := msg.String(); len(s) == 1 && s >= " " {
			m.search += s
			m.refreshTables()
		}
	}
	return m, nil
}

// onViewChange loads whatever the newly active view needs.
func (m *Model) onViewChange() tea.Cmd {
	m.detail = nil
	switch m.active {
	case ViewAssets:
		if m.assetRows == nil && !m.loadingArt {
			m.loadingArt = true
			return m.loadAssets()
		}
	case ViewDrive:
		if !m.loadingDrive {
			m.loadingDrive = true
			return m.loadDrive()
		}
	}
	return nil
}

func (m *Model) refreshAll() tea.Cmd {
	m.loadingCat, m.loadingDrive = true, true
	cmds := []tea.Cmd{m.loadCatalog(), m.loadDrive()}
	if m.active == ViewAssets {
		m.loadingArt = true
		cmds = append(cmds, m.loadAssets())
	}
	return tea.Batch(cmds...)
}

// activeTable returns the table the active view owns, or nil.
func (m *Model) activeTable() *components.Table {
	switch m.active {
	case ViewPS2Sources:
		return m.ps2Table
	case ViewPS1Sources:
		return m.ps1Table
	case ViewInstalled:
		return m.instTable
	case ViewAssets:
		return m.artTable
	}
	return nil
}

// layout resizes the tables for the current terminal size.
func (m *Model) layout() {
	w := m.contentWidth()
	h := m.contentHeight()
	for _, t := range []*components.Table{m.ps2Table, m.ps1Table, m.instTable, m.artTable} {
		t.SetSize(w, h)
	}
}

// Chrome heights: header, footer, status line and the blank lines around the
// content area.
const (
	headerHeight = 1
	footerHeight = 1
	statusHeight = 1
)

// narrowLayout reports whether the terminal is too small for the sidebar, in
// which case only the active view is drawn and it gets the full width.
func (m *Model) narrowLayout() bool { return m.width < 60 }

func (m *Model) contentWidth() int {
	w := m.width
	if !m.narrowLayout() {
		w -= sidebarWidth + 3
	}
	if w < 20 {
		w = 20
	}
	return w
}

// wrap word-wraps text to the content width, so a fixed sentence in an empty
// state cannot overflow a narrow terminal.
func (m *Model) wrap(s string) string {
	return lipgloss.NewStyle().Width(m.contentWidth()).Render(s)
}

func (m *Model) contentHeight() int {
	h := m.height - headerHeight - footerHeight - statusHeight - 2
	if h < 3 {
		h = 3
	}
	return h
}

// View renders the interface.
func (m *Model) View() string {
	if m.quitting {
		return ""
	}
	if m.width == 0 {
		return "starting…"
	}
	// Below this the split layout stops being readable, so the sidebar is
	// dropped and only the active view is drawn.
	narrow := m.narrowLayout()

	body := m.renderBody()
	var main string
	if narrow {
		main = body
	} else {
		main = lipgloss.JoinHorizontal(lipgloss.Top,
			m.renderSidebar(),
			lipgloss.NewStyle().Width(1).Render(""),
			lipgloss.NewStyle().Width(m.contentWidth()).Render(body))
	}

	screen := strings.Join([]string{
		components.Header(m.width, "ps2hdd", m.headerRight()),
		main,
		m.renderStatus(),
		components.Footer(m.width, m.hints()),
	}, "\n")

	if m.dialog != nil {
		return m.dialog.View(m.width, m.height)
	}
	return screen
}

func (m *Model) headerRight() string {
	switch {
	case m.driveErr != nil:
		return components.StyleDanger.Render("HDD unavailable")
	case m.loadingDrive && m.driveStatus.DevicePath == "":
		return components.StyleMuted.Render("checking the HDD…")
	case m.driveStatus.DevicePath == "":
		return components.StyleWarning.Render("no HDD configured")
	}
	name := m.driveStatus.Model
	if name == "" {
		name = m.driveStatus.DevicePath
	}
	return fmt.Sprintf("%s  %s  %s",
		components.Truncate(name, 28),
		components.StyleMuted.Render(model.HumanSize(m.driveStatus.FreeBytes)+" free"),
		components.StyleSuccess.Render("READY"))
}

func (m *Model) renderSidebar() string {
	var b strings.Builder
	for v := View(0); v < numViews; v++ {
		label := fmt.Sprintf(" %d %s", int(v)+1, v.Name())
		if badge := m.sidebarBadge(v); badge != "" {
			label += " " + badge
		}
		line := components.Pad(label, sidebarWidth)
		if v == m.active {
			b.WriteString(components.StyleSelected.Render(line))
		} else {
			b.WriteString(components.StyleMuted.Render(line))
		}
		if v < numViews-1 {
			b.WriteByte('\n')
		}
	}
	// Pad the sidebar to the content height so the layout does not jump.
	lines := strings.Count(b.String(), "\n") + 1
	for i := lines; i < m.contentHeight(); i++ {
		b.WriteString("\n" + strings.Repeat(" ", sidebarWidth))
	}
	return b.String()
}

// sidebarBadge shows a count next to a view when it has something to say.
func (m *Model) sidebarBadge(v View) string {
	switch v {
	case ViewQueue:
		if n := m.queue.Pending(); n > 0 {
			return components.StyleAccent.Render(fmt.Sprintf("(%d)", n))
		}
	case ViewAssets:
		missing := 0
		for _, e := range m.catalog.Entries {
			if len(e.MissingAssets) > 0 {
				missing++
			}
		}
		if missing > 0 {
			return components.StyleWarning.Render(fmt.Sprintf("(%d)", missing))
		}
	}
	return ""
}

func (m *Model) renderBody() string {
	if m.detail != nil {
		return m.renderDetail(*m.detail)
	}
	switch m.active {
	case ViewPS2Sources:
		return m.renderSources(model.PlatformPS2)
	case ViewPS1Sources:
		return m.renderSources(model.PlatformPS1)
	case ViewInstalled:
		return m.renderInstalled()
	case ViewAssets:
		return m.renderAssets()
	case ViewQueue:
		return m.renderQueue()
	case ViewDrive:
		return m.renderDrive()
	case ViewSettings:
		return m.renderSettings()
	}
	return ""
}

func (m *Model) renderStatus() string {
	if m.searching {
		return components.StyleAccent.Render(" /" + m.search + "▏")
	}
	if m.status != "" {
		kind := components.DialogInfo
		if m.statusIsError {
			kind = components.DialogError
		}
		return components.StatusLine(m.width, kind, m.status)
	}
	if m.loadingCat {
		return components.StatusLine(m.width, components.DialogConfirm,
			components.Spinner(m.tickCount)+" reading the library…")
	}
	if len(m.warnings) > 0 {
		return components.StatusLine(m.width, components.DialogDanger, m.warnings[0].Error())
	}
	return components.StatusLine(m.width, components.DialogConfirm, "")
}

// helpText is deliberately compact: it has to fit inside a dialog on a
// 24-row terminal, which is the smallest size worth designing for.
func helpText() string {
	return strings.Join([]string{
		"Move    ↑↓ or k j   pgup/pgdn page   g/G first/last",
		"Views   tab, shift-tab, or 1-7",
		"Any     / search  esc clear  r refresh  ? help  q quit",
		"",
		"Sources     space select  a all  i install  enter details",
		"Installed   space select  d remove  a artwork  f filter",
		"Assets      space select  a sync  A all missing  f incomplete",
		"Queue       c cancel  R retry  x clear finished",
		"Drive       p partitions  s re-check PS1",
		"Settings    enter change  s save  u undo",
	}, "\n")
}
