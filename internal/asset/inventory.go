package asset

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/casmith/ps2hdd/internal/model"
)

// Inventory is what a mounted +OPL partition already holds, keyed by
// normalised game id.
type Inventory struct {
	present map[string]map[model.AssetType]bool
}

// NewInventory returns an empty inventory.
func NewInventory() *Inventory {
	return &Inventory{present: map[string]map[model.AssetType]bool{}}
}

// Scan reads the ART and CFG directories of a mounted +OPL partition.
//
// A missing directory is not an error: a fresh +OPL partition has neither, and
// the correct answer is "nothing is present", not a failure.
func Scan(oplMount string) (*Inventory, error) {
	inv := NewInventory()
	if err := inv.scanArt(filepath.Join(oplMount, ArtDir)); err != nil {
		return nil, err
	}
	if err := inv.scanCfg(filepath.Join(oplMount, CfgDir)); err != nil {
		return nil, err
	}
	return inv, nil
}

func (i *Inventory) scanArt(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, t, ok := ParseArtFilename(e.Name())
		if !ok {
			continue
		}
		i.mark(id, t)
	}
	return nil
}

func (i *Inventory) scanCfg(dir string) error {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if id, ok := ParseCfgFilename(e.Name()); ok {
			i.mark(id, model.AssetConfig)
		}
	}
	return nil
}

func (i *Inventory) mark(gameID string, t model.AssetType) {
	key := model.NormalizeGameID(gameID)
	if i.present[key] == nil {
		i.present[key] = map[model.AssetType]bool{}
	}
	i.present[key][t] = true
}

// Status returns the asset status of one game.
func (i *Inventory) Status(gameID string) model.AssetStatus {
	key := model.NormalizeGameID(gameID)
	out := model.AssetStatus{Present: map[model.AssetType]bool{}}
	for t, ok := range i.present[key] {
		out.Present[t] = ok
	}
	return out
}

// Missing lists the wanted asset types a game does not have.
func (i *Inventory) Missing(gameID string, want []model.AssetType) []model.AssetType {
	return i.Status(gameID).Missing(want)
}

// Games reports how many distinct games the inventory saw.
func (i *Inventory) Games() int { return len(i.present) }
