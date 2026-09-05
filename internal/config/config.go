package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"

	"github.com/casmith/ps2hdd/internal/model"
)

// Config is the on-disk configuration, ~/.config/ps2hdd/config.toml.
type Config struct {
	// Device is a /dev/disk/by-id/... path. Unstable kernel names such as
	// /dev/sdb are rejected by Validate: they are reassigned across boots and
	// persisting one is how a tool ends up writing to the wrong disk.
	Device string `toml:"device,omitempty" json:"device,omitempty"`

	Sources SourcesConfig `toml:"sources" json:"sources"`
	Install InstallConfig `toml:"install" json:"install"`
	Assets  AssetsConfig  `toml:"assets" json:"assets"`
	Tools   ToolsConfig   `toml:"tools" json:"tools"`
	TUI     TUIConfig     `toml:"tui" json:"tui"`

	// path is where this config was loaded from; not serialised.
	path string
}

// SourcesConfig points at directories to browse for installable images. They
// are never treated as a record of what is installed: the HDD is the only
// authority on that.
type SourcesConfig struct {
	PS2 string `toml:"ps2,omitempty" json:"ps2,omitempty"`
	PS1 string `toml:"ps1,omitempty" json:"ps1,omitempty"`
}

// InstallConfig controls install-time behaviour.
type InstallConfig struct {
	SyncAssets         bool `toml:"sync_assets" json:"sync_assets"`
	VerifyAfterInstall bool `toml:"verify_after_install" json:"verify_after_install"`
	// CanonicalTitles looks a game's real name up from the serial on its disc
	// rather than taking whatever the file happened to be called. It needs the
	// network, and falls back to the filename when there is none.
	CanonicalTitles bool `toml:"canonical_titles" json:"canonical_titles"`
	// Widescreen turns on POPStarter's GTE widescreen hack for every PS1
	// title installed. Off by default, and deliberately: it corrects 3D
	// geometry but leaves HUDs, fonts, menus and 2D backgrounds stretched, and
	// some games do not run with it at all. `--widescreen` overrides this per
	// install, and the setting can be changed afterwards by editing one
	// CHEATS.TXT rather than reinstalling.
	Widescreen bool `toml:"widescreen" json:"widescreen"`
	// Prefetch is how many archived titles may be unpacked at once during a
	// bulk install, counting the one being written. 2 unpacks the next while
	// the current one is injected, which is what hides the decompression
	// behind the write; 1 or 0 disables it.
	//
	// It is a disk budget more than a thread count. Each unpacked title is up
	// to 4.7 GB in install.scratch_dir, so raising it costs that much space
	// per extra slot and buys nothing once the writer never waits.
	Prefetch int `toml:"prefetch" json:"prefetch"`
	// ScratchDir is where an archived image is decompressed before it is
	// injected. Empty means the cache directory.
	//
	// It matters where this points. A DVD rip is up to 4.7 GB, and on a
	// distribution that mounts /tmp as tmpfs the default temporary directory
	// is RAM. The cache directory is on disk, and this exists so a user with
	// a small home partition can send it somewhere larger.
	ScratchDir string `toml:"scratch_dir,omitempty" json:"scratch_dir,omitempty"`
}

// AssetsConfig selects the artwork provider and the slots to fetch.
type AssetsConfig struct {
	// Provider is a registered provider name, e.g. "ps2-covers" or "local".
	Provider string `toml:"provider" json:"provider"`
	// Mirror is the directory a "local" provider reads from.
	Mirror string `toml:"mirror,omitempty" json:"mirror,omitempty"`
	// Templates lets a user point the http provider at another database
	// without a code change: keys are art types (COV, BG, ...) and values are
	// URL templates understood by internal/asset/provider.
	Templates map[string]string `toml:"templates,omitempty" json:"templates,omitempty"`

	// Covers is the front cover, COV. BackCovers is COV2, kept separate
	// because the two are not equally available: several databases hold front
	// covers and nothing else, and one flag for both meant such a setup could
	// never report a complete library.
	Covers     bool `toml:"covers" json:"covers"`
	BackCovers bool `toml:"back_covers" json:"back_covers"`
	// Discs is the disc image, ICO. OPL Manager labels this slot "Disc"; it
	// is a 64x64 picture of the disc, not an application icon.
	Discs       bool `toml:"discs" json:"discs"`
	Backgrounds bool `toml:"backgrounds" json:"backgrounds"`
	Screenshots bool `toml:"screenshots" json:"screenshots"`
	Logos       bool `toml:"logos" json:"logos"`
	Spines      bool `toml:"spines" json:"spines"`
	// Config controls syncing per-game +OPL/CFG entries.
	Config bool `toml:"config" json:"config"`

	// Icons is the former name of Discs, read so an existing config keeps
	// working. It is a pointer because absent and false have to be told
	// apart: an unmarshal over a populated default cannot otherwise honour
	// someone who wrote `icons = false`. normalise folds it into Discs.
	Icons *bool `toml:"icons,omitempty" json:"-"`
}

// ToolsConfig overrides external executable locations. Empty means "look the
// name up on PATH".
type ToolsConfig struct {
	HDLDump  string `toml:"hdl_dump,omitempty" json:"hdl_dump,omitempty"`
	PFSFuse  string `toml:"pfsfuse,omitempty" json:"pfsfuse,omitempty"`
	PFSShell string `toml:"pfsshell,omitempty" json:"pfsshell,omitempty"`
	// Cue2POPS, when set, is used for BIN/CUE to VCD conversion instead of the
	// built-in converter. See docs/compatibility.md.
	Cue2POPS string `toml:"cue2pops,omitempty" json:"cue2pops,omitempty"`
	// Sudo runs privileged external commands through sudo. Raw block device
	// access usually needs it; the TUI itself should not run as root.
	Sudo bool `toml:"sudo" json:"sudo"`
}

// TUIConfig holds interface preferences.
type TUIConfig struct {
	ConfirmDestructiveActions bool `toml:"confirm_destructive_actions" json:"confirm_destructive_actions"`
}

// Default returns a configuration with conservative defaults: confirmations
// on, verification on, and the three art slots the default provider can
// actually fill for essentially every title -- front cover, back cover and the
// disc image.
//
// Enabling a slot the provider cannot supply is what makes a library report as
// permanently incomplete, so the defaults are chosen to be satisfiable.
func Default() Config {
	return Config{
		Install: InstallConfig{SyncAssets: true, VerifyAfterInstall: true, Prefetch: 2, CanonicalTitles: true},
		Assets: AssetsConfig{
			// opl-art is the default because it is the only source that can
			// satisfy the slots enabled here. A default configuration that
			// reports every game as incomplete, forever, teaches people to
			// ignore the report.
			Provider:   "opl-art",
			Covers:     true,
			BackCovers: true,
			Discs:      true,
		},
		TUI: TUIConfig{ConfirmDestructiveActions: true},
	}
}

// WantedAssets lists the asset types enabled by this configuration, in a
// stable order.
func (c Config) WantedAssets() []model.AssetType {
	var out []model.AssetType
	add := func(on bool, t model.AssetType) {
		if on {
			out = append(out, t)
		}
	}
	add(c.Assets.Covers, model.AssetCover)
	add(c.Assets.BackCovers, model.AssetCoverBack)
	add(c.Assets.Spines, model.AssetSpine)
	add(c.Assets.Backgrounds, model.AssetBackground)
	add(c.Assets.Screenshots, model.AssetScreen)
	add(c.Assets.Screenshots, model.AssetScreen2)
	add(c.Assets.Discs, model.AssetIcon)
	add(c.Assets.Logos, model.AssetLogo)
	add(c.Assets.Config, model.AssetConfig)
	return out
}

// Path reports where the config was loaded from or will be written to.
func (c Config) Path() string { return c.path }

// SetPath overrides the save location.
func (c *Config) SetPath(p string) { c.path = p }

// ErrNotConfigured is returned by callers that need a device but have none.
var ErrNotConfigured = errors.New("no PS2 HDD configured: run `ps2hdd detect --configure` or set device in the config file")

// Load reads the config at path. When path is empty the default location is
// used. A missing file is not an error: defaults are returned so a first run
// works without any setup.
func Load(path string) (Config, error) {
	cfg := Default()
	var err error
	if path == "" {
		if path, err = DefaultPath(); err != nil {
			return cfg, err
		}
	}
	cfg.path = path

	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil
		}
		return cfg, fmt.Errorf("read config %s: %w", path, err)
	}
	if err := toml.Unmarshal(data, &cfg); err != nil {
		return cfg, fmt.Errorf("parse config %s: %w", path, err)
	}
	cfg.path = path
	cfg.normalise()
	return cfg, nil
}

// normalise expands ~ in path-valued fields so the rest of the program only
// ever sees absolute paths.
func (c *Config) normalise() {
	c.Sources.PS2 = ExpandPath(c.Sources.PS2)
	c.Sources.PS1 = ExpandPath(c.Sources.PS1)
	c.Assets.Mirror = ExpandPath(c.Assets.Mirror)
	c.Device = strings.TrimSpace(c.Device)
	if c.Assets.Provider == "" {
		c.Assets.Provider = "opl-art"
	}
	// `icons` was the old name for the disc slot. Fold it in so an existing
	// config keeps asking for exactly what it asked for before, including a
	// deliberate `icons = false`.
	if c.Assets.Icons != nil {
		c.Assets.Discs = *c.Assets.Icons
		c.Assets.Icons = nil
	}
}

// Save writes the config atomically, creating the parent directory.
func (c Config) Save() error {
	if c.path == "" {
		p, err := DefaultPath()
		if err != nil {
			return err
		}
		c.path = p
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0o755); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	data, err := toml.Marshal(c)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("replace config: %w", err)
	}
	return nil
}

// ValidateDevice rejects device identifiers that are not stable across boots.
// This is a safety invariant, not a style preference: /dev/sdb can point at a
// different disk after a reboot or a hotplug.
func ValidateDevice(dev string) error {
	dev = strings.TrimSpace(dev)
	if dev == "" {
		return ErrNotConfigured
	}
	if !filepath.IsAbs(dev) {
		return fmt.Errorf("device %q must be an absolute path", dev)
	}
	if strings.HasPrefix(dev, "/dev/disk/by-id/") ||
		strings.HasPrefix(dev, "/dev/disk/by-uuid/") ||
		strings.HasPrefix(dev, "/dev/disk/by-path/") ||
		strings.HasPrefix(dev, "/dev/disk/by-partuuid/") {
		return nil
	}
	// A regular file is a disk image, which is how the test suite and users
	// working from a dump address a HDD. Those are stable by path.
	if fi, err := os.Stat(dev); err == nil && fi.Mode().IsRegular() {
		return nil
	}
	return fmt.Errorf("device %q is not a stable identifier: use a /dev/disk/by-id/... path (kernel names such as /dev/sdb are reassigned between boots)", dev)
}

// Validate checks the whole configuration. It tolerates an unset device so
// that read-only commands and first-run setup still work.
func (c Config) Validate() error {
	if c.Device != "" {
		if err := ValidateDevice(c.Device); err != nil {
			return err
		}
	}
	for name, dir := range map[string]string{"sources.ps2": c.Sources.PS2, "sources.ps1": c.Sources.PS1} {
		if dir == "" {
			continue
		}
		if fi, err := os.Stat(dir); err != nil {
			return fmt.Errorf("%s: %s: %w", name, dir, err)
		} else if !fi.IsDir() {
			return fmt.Errorf("%s: %s is not a directory", name, dir)
		}
	}
	return nil
}
