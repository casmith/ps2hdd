package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/tui/components"
)

// settingKind decides how a field is edited.
type settingKind int

const (
	settingText settingKind = iota
	settingBool
	settingChoice
)

// setting is one editable configuration field.
type setting struct {
	Key   string
	Label string
	Help  string
	Kind  settingKind
	// Choices are the valid values for settingChoice.
	Choices []string
	// get and set read and write the field on a config value.
	get func(config.Config) string
	set func(*config.Config, string) error
}

// settingsModel holds the Settings view's editing state.
//
// The whole point of this view is that a user never has to open the TOML file
// by hand, so every field the config file exposes and that is safe to change
// from a running interface appears here. The device is deliberately read-only:
// choosing the wrong disk is the one mistake this program exists to prevent,
// and `ps2hdd detect --configure` is the path that validates it.
type settingsModel struct {
	cfg      config.Config
	original config.Config
	cursor   int
	editing  bool
	buffer   string
	fields   []setting
}

func newSettingsModel(cfg config.Config) *settingsModel {
	s := &settingsModel{cfg: cfg, original: cfg}
	s.fields = []setting{
		{
			Key: "sources.ps2", Label: "PS2 source directory", Kind: settingText,
			Help: "Where to browse for PS2 disc images. Not a record of what is installed.",
			get:  func(c config.Config) string { return c.Sources.PS2 },
			set: func(c *config.Config, v string) error {
				c.Sources.PS2 = config.ExpandPath(v)
				return nil
			},
		},
		{
			Key: "sources.ps1", Label: "PS1 source directory", Kind: settingText,
			Help: "Where to browse for PS1 BIN/CUE rips.",
			get:  func(c config.Config) string { return c.Sources.PS1 },
			set: func(c *config.Config, v string) error {
				c.Sources.PS1 = config.ExpandPath(v)
				return nil
			},
		},
		{
			Key: "assets.provider", Label: "Artwork provider", Kind: settingChoice,
			Choices: []string{"ps2-covers", "local", "http"},
			Help:    "ps2-covers fetches front covers from the community database; local reads a directory.",
			get:     func(c config.Config) string { return c.Assets.Provider },
			set:     func(c *config.Config, v string) error { c.Assets.Provider = v; return nil },
		},
		{
			Key: "assets.mirror", Label: "Artwork mirror", Kind: settingText,
			Help: "A local artwork directory, tried before the remote provider.",
			get:  func(c config.Config) string { return c.Assets.Mirror },
			set: func(c *config.Config, v string) error {
				c.Assets.Mirror = config.ExpandPath(v)
				return nil
			},
		},
		boolSetting("assets.covers", "Sync front covers",
			func(c config.Config) bool { return c.Assets.Covers },
			func(c *config.Config, v bool) { c.Assets.Covers = v }),
		boolSetting("assets.backgrounds", "Sync backgrounds",
			func(c config.Config) bool { return c.Assets.Backgrounds },
			func(c *config.Config, v bool) { c.Assets.Backgrounds = v }),
		boolSetting("assets.screenshots", "Sync screenshots",
			func(c config.Config) bool { return c.Assets.Screenshots },
			func(c *config.Config, v bool) { c.Assets.Screenshots = v }),
		boolSetting("assets.back_covers", "Sync back covers",
			func(c config.Config) bool { return c.Assets.BackCovers },
			func(c *config.Config, v bool) { c.Assets.BackCovers = v }),
		boolSetting("assets.discs", "Sync disc images",
			func(c config.Config) bool { return c.Assets.Discs },
			func(c *config.Config, v bool) { c.Assets.Discs = v }),
		boolSetting("assets.logos", "Sync logos",
			func(c config.Config) bool { return c.Assets.Logos },
			func(c *config.Config, v bool) { c.Assets.Logos = v }),
		boolSetting("assets.config", "Sync per-game CFG",
			func(c config.Config) bool { return c.Assets.Config },
			func(c *config.Config, v bool) { c.Assets.Config = v }),
		boolSetting("install.sync_assets", "Sync artwork after install",
			func(c config.Config) bool { return c.Install.SyncAssets },
			func(c *config.Config, v bool) { c.Install.SyncAssets = v }),
		boolSetting("install.verify_after_install", "Verify after install",
			func(c config.Config) bool { return c.Install.VerifyAfterInstall },
			func(c *config.Config, v bool) { c.Install.VerifyAfterInstall = v }),
		boolSetting("tui.confirm_destructive_actions", "Confirm destructive actions",
			func(c config.Config) bool { return c.TUI.ConfirmDestructiveActions },
			func(c *config.Config, v bool) { c.TUI.ConfirmDestructiveActions = v }),
		boolSetting("tools.sudo", "Run privileged tools with sudo",
			func(c config.Config) bool { return c.Tools.Sudo },
			func(c *config.Config, v bool) { c.Tools.Sudo = v }),
	}
	return s
}

func boolSetting(key, label string, get func(config.Config) bool, set func(*config.Config, bool)) setting {
	return setting{
		Key: key, Label: label, Kind: settingBool,
		get: func(c config.Config) string {
			if get(c) {
				return "true"
			}
			return "false"
		},
		set: func(c *config.Config, v string) error {
			set(c, v == "true")
			return nil
		},
	}
}

// dirty reports whether anything changed since the last save.
func (s *settingsModel) dirty() bool {
	for _, f := range s.fields {
		if f.get(s.cfg) != f.get(s.original) {
			return true
		}
	}
	return false
}

func (m *Model) handleSettingsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	s := m.settings
	if s.editing {
		switch {
		case keyMatches(msg, "enter"):
			f := s.fields[s.cursor]
			if err := f.set(&s.cfg, strings.TrimSpace(s.buffer)); err != nil {
				return m, m.statusFor(err.Error(), true)
			}
			s.editing = false
		case keyMatches(msg, "esc"):
			s.editing = false
		case keyMatches(msg, "backspace"):
			if s.buffer != "" {
				r := []rune(s.buffer)
				s.buffer = string(r[:len(r)-1])
			}
		default:
			if str := msg.String(); len(str) == 1 && str >= " " {
				s.buffer += str
			}
		}
		return m, nil
	}

	switch {
	case keyMatches(msg, "up", "k"):
		s.cursor--
	case keyMatches(msg, "down", "j"):
		s.cursor++
	case keyMatches(msg, "enter", " "):
		f := s.fields[s.cursor]
		switch f.Kind {
		case settingBool:
			v := "true"
			if f.get(s.cfg) == "true" {
				v = "false"
			}
			_ = f.set(&s.cfg, v)
		case settingChoice:
			cur := f.get(s.cfg)
			next := f.Choices[0]
			for i, c := range f.Choices {
				if c == cur {
					next = f.Choices[(i+1)%len(f.Choices)]
					break
				}
			}
			_ = f.set(&s.cfg, next)
		default:
			s.editing = true
			s.buffer = f.get(s.cfg)
		}
	case keyMatches(msg, "s"):
		if !s.dirty() {
			return m, m.statusFor("Nothing to save.", false)
		}
		cfg := s.cfg
		s.original = cfg
		// Rebuilding the asset columns matters because the enabled slots
		// decide which columns the Assets view shows.
		m.artTable.Columns = assetColumns(cfg.WantedAssets())
		return m, m.saveConfig(cfg)
	case keyMatches(msg, "u"):
		s.cfg = s.original
	}
	if s.cursor < 0 {
		s.cursor = 0
	}
	if s.cursor >= len(s.fields) {
		s.cursor = len(s.fields) - 1
	}
	return m, nil
}

func (m *Model) renderSettings() string {
	s := m.settings
	var b strings.Builder
	b.WriteString(components.StyleHeader.Render("Settings"))
	b.WriteString("  " + components.StyleMuted.Render(
		components.Truncate(s.cfg.Path(), m.contentWidth()-14)))
	if s.dirty() {
		b.WriteString("  " + components.StyleWarning.Render("unsaved"))
	}
	b.WriteString("\n\n")

	// The device is shown but not editable here; changing it is what
	// `detect --configure` is for, and it validates the choice.
	labelWidth := 30
	if w := m.contentWidth() / 2; w < labelWidth {
		labelWidth = w
	}
	valueWidth := m.contentWidth() - labelWidth - 4
	if valueWidth < 8 {
		valueWidth = 8
	}

	b.WriteString("  " + components.StyleMuted.Render(components.Pad("HDD", labelWidth)) + "  ")
	if s.cfg.Device == "" {
		b.WriteString(components.StyleWarning.Render("not configured"))
	} else {
		b.WriteString(components.Truncate(s.cfg.Device, valueWidth))
	}
	b.WriteString("\n  " + components.StyleMuted.Render(components.Pad("", labelWidth)) + "  " +
		components.StyleMuted.Render(components.Truncate("change with `ps2hdd detect --configure`", valueWidth)))
	b.WriteString("\n\n")

	// Only as many rows as fit are drawn, scrolled to keep the cursor visible.
	visible := m.contentHeight() - 8
	if visible < 3 {
		visible = 3
	}
	start := 0
	if s.cursor >= visible {
		start = s.cursor - visible + 1
	}
	end := start + visible
	if end > len(s.fields) {
		end = len(s.fields)
	}

	for i := start; i < end; i++ {
		f := s.fields[i]
		value := f.get(s.cfg)
		if f.Kind == settingBool {
			if value == "true" {
				value = components.StyleSuccess.Render("on")
			} else {
				value = components.StyleMuted.Render("off")
			}
		} else if value == "" {
			value = components.StyleMuted.Render("(not set)")
		}
		if s.editing && i == s.cursor {
			value = components.StyleAccent.Render(s.buffer + "▏")
		}
		line := "  " + components.Pad(f.Label, labelWidth) + "  " +
			components.Truncate(value, valueWidth)
		if i == s.cursor {
			b.WriteString(components.StyleSelected.Render(components.Pad(line, m.contentWidth())))
		} else {
			b.WriteString(line)
		}
		b.WriteByte('\n')
	}

	if help := s.fields[s.cursor].Help; help != "" {
		b.WriteString("\n" + components.StyleMuted.Render("  "+
			components.Truncate(help, m.contentWidth()-4)))
	}
	if s.editing {
		b.WriteString("\n\n" + components.StyleMuted.Render("  enter to accept · esc to cancel"))
	} else {
		b.WriteString(fmt.Sprintf("\n\n  %s",
			components.StyleMuted.Render("enter to change · s to save · u to undo")))
	}
	return b.String()
}
