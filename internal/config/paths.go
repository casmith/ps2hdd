// Package config loads, validates and saves the ps2hdd TOML configuration and
// resolves the XDG directories the application uses.
package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const appName = "ps2hdd"

// ConfigDir is $XDG_CONFIG_HOME/ps2hdd (default ~/.config/ps2hdd).
func ConfigDir() (string, error) { return xdgDir("XDG_CONFIG_HOME", ".config") }

// CacheDir is $XDG_CACHE_HOME/ps2hdd (default ~/.cache/ps2hdd).
func CacheDir() (string, error) { return xdgDir("XDG_CACHE_HOME", ".cache") }

// StateDir is $XDG_STATE_HOME/ps2hdd (default ~/.local/state/ps2hdd) and holds
// the log file.
func StateDir() (string, error) { return xdgDir("XDG_STATE_HOME", filepath.Join(".local", "state")) }

func xdgDir(env, fallback string) (string, error) {
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, appName), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, fallback, appName), nil
}

// DefaultPath is the config file location, ~/.config/ps2hdd/config.toml.
func DefaultPath() (string, error) {
	dir, err := ConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// RuntimeDir is where PFS mountpoints are created. It prefers
// $XDG_RUNTIME_DIR/ps2hdd and falls back to /tmp/ps2hdd-<uid>.
//
// Mountpoints expose the contents of another user's game HDD, so both the
// fallback root and the per-run directory are created 0700 and the fallback is
// rejected outright if it already exists owned by somebody else.
func RuntimeDir() (string, error) {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" && filepath.IsAbs(v) {
		dir := filepath.Join(v, appName)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return "", fmt.Errorf("create runtime directory %s: %w", dir, err)
		}
		return dir, nil
	}
	dir := filepath.Join(os.TempDir(), fmt.Sprintf("%s-%d", appName, os.Getuid()))
	if err := mkdirOwned(dir); err != nil {
		return "", err
	}
	return dir, nil
}

// mkdirOwned creates dir 0700 and refuses to reuse a path that already exists
// but is not a directory owned by this user with no group or world access.
func mkdirOwned(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create runtime directory %s: %w", dir, err)
	}
	if err := checkPrivateDir(dir); err != nil {
		return err
	}
	// MkdirAll leaves an existing directory's mode alone.
	return os.Chmod(dir, 0o700)
}

// ExpandPath expands a leading ~ and makes the result absolute so that
// configured source directories behave the way users expect.
func ExpandPath(p string) string {
	p = strings.TrimSpace(p)
	if p == "" {
		return ""
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			p = filepath.Join(home, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
		}
	}
	if abs, err := filepath.Abs(p); err == nil {
		return abs
	}
	return p
}
