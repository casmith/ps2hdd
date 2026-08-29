package asset

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
)

// DownloadCache stores fetched artwork under ~/.cache/ps2hdd/art so that a
// re-sync, or a sync of the same title to a second HDD, does not hit the
// network again. The cache is disposable; deleting it costs bandwidth only.
type DownloadCache struct {
	root string
}

// OpenDownloadCache creates the cache directory.
func OpenDownloadCache() (*DownloadCache, error) {
	dir, err := config.CacheDir()
	if err != nil {
		return nil, err
	}
	root := filepath.Join(dir, "art")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create artwork cache: %w", err)
	}
	return &DownloadCache{root: root}, nil
}

// NewDownloadCacheAt returns a cache rooted at an explicit directory.
func NewDownloadCacheAt(root string) (*DownloadCache, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	return &DownloadCache{root: root}, nil
}

// Root reports the cache directory.
func (c *DownloadCache) Root() string { return c.root }

// key derives a stable filename for an asset. The source URL is hashed so that
// switching providers does not collide, and the game id and type are kept in
// the name so the cache is legible when a user looks inside it.
func (c *DownloadCache) key(a model.Asset) string {
	sum := sha256.Sum256([]byte(a.Source))
	return fmt.Sprintf("%s_%s_%s%s",
		model.OPLGameID(a.GameID), a.Type, hex.EncodeToString(sum[:6]), filepath.Ext(Filename(a.GameID, a.Type)))
}

// Path returns where an asset would be cached.
func (c *DownloadCache) Path(a model.Asset) string {
	return filepath.Join(c.root, c.key(a))
}

// Get returns the cached file if it is present and non-empty.
func (c *DownloadCache) Get(a model.Asset) (string, bool) {
	p := c.Path(a)
	fi, err := os.Stat(p)
	if err != nil || !fi.Mode().IsRegular() || fi.Size() == 0 {
		return "", false
	}
	return p, true
}

// Put stores an asset's bytes and returns the cached path. The write is
// atomic, so an interrupted download never leaves a truncated file that a
// later run would treat as a valid cache hit.
func (c *DownloadCache) Put(a model.Asset, r io.Reader) (string, error) {
	dest := c.Path(a)
	tmp, err := os.CreateTemp(c.root, ".dl-*")
	if err != nil {
		return "", err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	n, err := io.Copy(tmp, io.LimitReader(r, maxAssetBytes+1))
	if err != nil {
		return "", err
	}
	if n == 0 {
		return "", fmt.Errorf("artwork source returned an empty file")
	}
	if n > maxAssetBytes {
		return "", fmt.Errorf("artwork is larger than %d bytes, which is not a cover image", maxAssetBytes)
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmpName, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// maxAssetBytes caps a download. OPL artwork is at most a few hundred
// kilobytes; the limit keeps a misconfigured URL from filling the cache.
const maxAssetBytes = 16 << 20

// Clean removes every cached download.
func (c *DownloadCache) Clean() (int, error) {
	entries, err := os.ReadDir(c.root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(c.root, e.Name())); err == nil {
			n++
		}
	}
	return n, nil
}

// Size reports the total bytes held in the cache.
func (c *DownloadCache) Size() (int64, error) {
	var total int64
	entries, err := os.ReadDir(c.root)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil
		}
		return 0, err
	}
	for _, e := range entries {
		if fi, err := e.Info(); err == nil {
			total += fi.Size()
		}
	}
	return total, nil
}
