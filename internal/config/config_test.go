package config_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
)

func TestLoadMissingFileYieldsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// A first run must work with no setup at all.
	if cfg.Assets.Provider != "ps2-covers" {
		t.Errorf("provider = %q", cfg.Assets.Provider)
	}
	if !cfg.TUI.ConfirmDestructiveActions {
		t.Error("confirmations default to off; they must default to on")
	}
	if !cfg.Install.VerifyAfterInstall {
		t.Error("verification defaults to off")
	}
	if cfg.Path() != path {
		t.Errorf("Path = %q", cfg.Path())
	}
}

func TestLoadParsesTOML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	src := filepath.Join(dir, "ps2")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	body := `device = "/dev/disk/by-id/ata-WDC_WD1200JB-00REA0_WD-WCANM1234567"

[sources]
ps2 = "` + src + `"

[install]
sync_assets = false
verify_after_install = true

[assets]
provider = "local"
mirror = "` + dir + `"
covers = true
backgrounds = true

[assets.templates]
BG = "https://example/{serial}_BG.png"

[tui]
confirm_destructive_actions = false
`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Device != "/dev/disk/by-id/ata-WDC_WD1200JB-00REA0_WD-WCANM1234567" {
		t.Errorf("device = %q", cfg.Device)
	}
	if cfg.Sources.PS2 != src {
		t.Errorf("sources.ps2 = %q", cfg.Sources.PS2)
	}
	if cfg.Install.SyncAssets {
		t.Error("install.sync_assets should be false")
	}
	if cfg.Assets.Provider != "local" || cfg.Assets.Mirror != dir {
		t.Errorf("assets = %+v", cfg.Assets)
	}
	if cfg.Assets.Templates["BG"] == "" {
		t.Errorf("templates = %v", cfg.Assets.Templates)
	}
	if cfg.TUI.ConfirmDestructiveActions {
		t.Error("tui.confirm_destructive_actions should be false")
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestLoadRejectsBrokenTOML(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("device = \nthis is not toml"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := config.Load(path); err == nil {
		t.Fatal("Load accepted broken TOML")
	}
}

func TestSaveRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "config.toml")
	cfg := config.Default()
	cfg.SetPath(path)
	cfg.Device = "/dev/disk/by-id/ata-X_Y"
	cfg.Assets.Backgrounds = true
	if err := cfg.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}
	back, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if back.Device != cfg.Device || !back.Assets.Backgrounds {
		t.Errorf("round trip lost data: %+v", back)
	}
	// No temporary file may survive a successful save.
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Error("a .tmp file was left behind")
	}
}

// Persisting an unstable device identifier is the mistake that leads to
// writing to the wrong disk, so it is rejected outright.
func TestValidateDeviceRejectsKernelNames(t *testing.T) {
	for _, dev := range []string{"/dev/sdb", "/dev/nvme0n1", "sdb", "./sdb"} {
		if err := config.ValidateDevice(dev); err == nil {
			t.Errorf("ValidateDevice(%q) accepted an unstable identifier", dev)
		}
	}
	for _, dev := range []string{
		"/dev/disk/by-id/ata-WDC_WD1200JB_WD-WCANM1",
		"/dev/disk/by-uuid/1234",
		"/dev/disk/by-path/pci-0000:00:1f.2-ata-2",
	} {
		if err := config.ValidateDevice(dev); err != nil {
			t.Errorf("ValidateDevice(%q) = %v", dev, err)
		}
	}
	if err := config.ValidateDevice(""); !errors.Is(err, config.ErrNotConfigured) {
		t.Errorf("empty device error = %v", err)
	}
}

// A disk image is a legitimate target and is stable by path.
func TestValidateDeviceAcceptsImage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hdd.img")
	if err := os.WriteFile(path, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := config.ValidateDevice(path); err != nil {
		t.Errorf("ValidateDevice(image) = %v", err)
	}
}

func TestValidateRejectsMissingSourceDir(t *testing.T) {
	cfg := config.Default()
	cfg.Sources.PS2 = filepath.Join(t.TempDir(), "nope")
	if err := cfg.Validate(); err == nil {
		t.Fatal("Validate accepted a missing source directory")
	}
}

func TestWantedAssets(t *testing.T) {
	cfg := config.Default() // covers and config on, everything else off
	want := cfg.WantedAssets()
	has := map[model.AssetType]bool{}
	for _, t := range want {
		has[t] = true
	}
	if !has[model.AssetCover] || !has[model.AssetConfig] {
		t.Errorf("defaults = %v", want)
	}
	if has[model.AssetBackground] || has[model.AssetIcon] {
		t.Errorf("disabled slots leaked in: %v", want)
	}

	cfg.Assets.Backgrounds = true
	cfg.Assets.Screenshots = true
	want = cfg.WantedAssets()
	var n int
	for _, t := range want {
		if t == model.AssetScreen || t == model.AssetScreen2 {
			n++
		}
	}
	if n != 2 {
		t.Errorf("screenshots should enable both SCR and SCR2: %v", want)
	}
}

func TestExpandPath(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home directory")
	}
	if got := config.ExpandPath("~/games"); got != filepath.Join(home, "games") {
		t.Errorf("ExpandPath(~/games) = %q", got)
	}
	if got := config.ExpandPath(""); got != "" {
		t.Errorf("ExpandPath(\"\") = %q", got)
	}
	if got := config.ExpandPath("relative"); !filepath.IsAbs(got) {
		t.Errorf("ExpandPath(relative) = %q, want an absolute path", got)
	}
}

func TestXDGDirs(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "c"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "ca"))
	t.Setenv("XDG_STATE_HOME", filepath.Join(base, "s"))

	for name, fn := range map[string]func() (string, error){
		"config": config.ConfigDir, "cache": config.CacheDir, "state": config.StateDir,
	} {
		dir, err := fn()
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !strings.HasPrefix(dir, base) || !strings.HasSuffix(dir, "ps2hdd") {
			t.Errorf("%s dir = %q", name, dir)
		}
	}
}

// The runtime directory holds mountpoints exposing another user's game HDD, so
// it is created private and a pre-existing world-accessible one is refused.
func TestRuntimeDirIsPrivate(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", base)
	dir, err := config.RuntimeDir()
	if err != nil {
		t.Fatalf("RuntimeDir: %v", err)
	}
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm()&0o077 != 0 {
		t.Errorf("runtime directory mode is %o, want no group or world access", fi.Mode().Perm())
	}
}

func TestRuntimeDirFallbackRefusesOpenDirectory(t *testing.T) {
	base := t.TempDir()
	t.Setenv("XDG_RUNTIME_DIR", "")
	t.Setenv("TMPDIR", base)
	// Plant a world-writable directory where the fallback would land.
	planted := filepath.Join(base, "ps2hdd-"+itoa(os.Getuid()))
	if err := os.MkdirAll(planted, 0o777); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(planted, 0o777); err != nil {
		t.Fatal(err)
	}
	if _, err := config.RuntimeDir(); err == nil {
		t.Fatal("RuntimeDir reused a world-accessible directory in a shared temp dir")
	}
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
