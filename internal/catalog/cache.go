// Package catalog scans source directories, reads the installed library from
// the HDD, and reconciles the two into the unified view the TUI and CLI show.
package catalog

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
)

// cacheVersion is bumped whenever the cached record's meaning changes, so an
// upgrade invalidates stale entries instead of misreading them.
const cacheVersion = 2

// Entry is one cached inspection result.
type Entry struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"mod_time"`
	// Game is the inspection result. Inspecting a large ISO means reading its
	// filesystem off a network share, which is what makes caching worthwhile.
	Game model.Game `json:"game"`
	// Err records an inspection failure so a directory full of non-games is
	// not re-examined on every launch. It is a message, not an error value.
	Err string `json:"err,omitempty"`
}

// fresh reports whether a cached entry still describes the file on disk.
// Path, size and modification time together are enough: a game image that
// changes without any of the three changing is not a case worth defending
// against, and hashing multi-gigabyte ISOs on every scan would defeat the
// point of the cache.
func (e Entry) fresh(fi os.FileInfo) bool {
	return e.Size == fi.Size() && e.ModTime.Equal(fi.ModTime())
}

// Cache stores source-scan results under ~/.cache/ps2hdd/source-index.
//
// The cache is disposable: deleting it costs a rescan and nothing else.
// Installed state is never cached, because the HDD is the only authority on
// what is installed.
type Cache struct {
	mu      sync.Mutex
	path    string
	entries map[string]Entry
	dirty   bool
	// disabled is set when the cache directory could not be created, in which
	// case scanning still works and simply does not persist.
	disabled bool
}

type cacheFile struct {
	Version int              `json:"version"`
	Entries map[string]Entry `json:"entries"`
}

// OpenCache loads the cache for a named index, e.g. "ps2" or "ps1".
func OpenCache(name string) (*Cache, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return &Cache{entries: map[string]Entry{}, disabled: true}, err
	}
	dir = filepath.Join(dir, "source-index")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return &Cache{entries: map[string]Entry{}, disabled: true},
			fmt.Errorf("create cache directory: %w", err)
	}
	c := &Cache{path: filepath.Join(dir, name+".json"), entries: map[string]Entry{}}
	data, err := os.ReadFile(c.path)
	if err != nil {
		return c, nil // a missing cache is the normal first-run case
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != cacheVersion {
		// A corrupt or outdated cache is discarded rather than repaired.
		return c, nil
	}
	if f.Entries != nil {
		c.entries = f.Entries
	}
	return c, nil
}

// NewMemoryCache returns a cache that never touches disk, for tests and for
// `--no-cache`.
func NewMemoryCache() *Cache {
	return &Cache{entries: map[string]Entry{}, disabled: true}
}

// Get returns a cached entry when it still matches the file on disk.
func (c *Cache) Get(path string, fi os.FileInfo) (Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.entries[path]
	if !ok || !e.fresh(fi) {
		return Entry{}, false
	}
	return e, true
}

// Put records an inspection result.
func (c *Cache) Put(path string, fi os.FileInfo, g model.Game, inspectErr error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e := Entry{Path: path, Size: fi.Size(), ModTime: fi.ModTime(), Game: g}
	if inspectErr != nil {
		e.Err = inspectErr.Error()
	}
	c.entries[path] = e
	c.dirty = true
}

// Prune drops entries for paths that were not seen in the latest scan, so a
// deleted directory does not leave the cache growing forever.
func (c *Cache) Prune(seen map[string]bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for path := range c.entries {
		if !seen[path] {
			delete(c.entries, path)
			c.dirty = true
		}
	}
}

// Save writes the cache back if anything changed.
func (c *Cache) Save() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.disabled || !c.dirty || c.path == "" {
		return nil
	}
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Entries: c.entries})
	if err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, c.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	c.dirty = false
	return nil
}

// Clear empties the cache.
func (c *Cache) Clear() error {
	c.mu.Lock()
	c.entries = map[string]Entry{}
	c.dirty = true
	path := c.path
	c.mu.Unlock()
	if path != "" {
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

// Len reports how many entries are cached.
func (c *Cache) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.entries)
}
