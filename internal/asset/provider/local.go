package provider

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// local serves artwork from a directory on the workstation: an OPL Manager art
// dump, a rsync of a friend's +OPL/ART, or a directory the user curates by
// hand. It is the offline half of the provider system and the escape hatch for
// art slots no public database covers.
//
// Files are matched by OPL's own naming convention, so a directory copied
// straight out of a working +OPL partition works unchanged. Both the OPL
// serial form and the dashed form are accepted, and .png, .jpg and .jpeg are
// all recognised.
type local struct {
	root string
}

func newLocal(o Options) (Provider, error) {
	if o.Mirror == "" {
		return nil, fmt.Errorf("the local artwork provider needs `mirror` set to a directory in [assets]")
	}
	fi, err := os.Stat(o.Mirror)
	if err != nil {
		return nil, fmt.Errorf("artwork mirror %s: %w", o.Mirror, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("artwork mirror %s is not a directory", o.Mirror)
	}
	return &local{root: o.Mirror}, nil
}

func (p *local) Name() string { return "local" }

// Supports is everything. A directory can hold any slot, so what is actually
// there is a per-game question that Lookup answers by looking.
func (p *local) Supports() []model.AssetType {
	return append(append([]model.AssetType{}, model.ArtTypes...), model.AssetConfig)
}

// searchDirs are the places inside a mirror an asset might live: the mirror
// root itself, and an ART or CFG subdirectory as found in a copied +OPL.
func (p *local) searchDirs(t model.AssetType) []string {
	sub := "ART"
	if t == model.AssetConfig {
		sub = "CFG"
	}
	return []string{p.root, filepath.Join(p.root, sub)}
}

func (p *local) candidates(game model.Game, t model.AssetType) []string {
	ids := []string{model.OPLGameID(game.GameID), model.DashedGameID(game.GameID)}
	var exts []string
	if t == model.AssetConfig {
		exts = []string{".cfg", ".CFG"}
	} else {
		exts = []string{".png", ".PNG", ".jpg", ".JPG", ".jpeg"}
	}
	var out []string
	for _, dir := range p.searchDirs(t) {
		for _, id := range ids {
			for _, ext := range exts {
				name := id + ext
				if t != model.AssetConfig {
					name = id + "_" + string(t) + ext
				}
				out = append(out, filepath.Join(dir, name))
			}
		}
	}
	return out
}

func (p *local) Lookup(ctx context.Context, game model.Game, want []model.AssetType) (model.AssetSet, error) {
	var set model.AssetSet
	if model.NormalizeGameID(game.GameID) == "" {
		return set, nil
	}
	for _, t := range want {
		for _, c := range p.candidates(game, t) {
			if fi, err := os.Stat(c); err == nil && fi.Mode().IsRegular() {
				set.Assets = append(set.Assets, model.Asset{
					Type:     t,
					GameID:   game.GameID,
					Platform: game.Platform,
					Source:   c,
				})
				break
			}
		}
	}
	return set, nil
}

func (p *local) Fetch(ctx context.Context, a model.Asset) (io.ReadCloser, error) {
	// Refuse to read outside the mirror, so a crafted asset record cannot turn
	// a sync into an arbitrary file copy.
	abs, err := filepath.Abs(a.Source)
	if err != nil {
		return nil, err
	}
	root, err := filepath.Abs(p.root)
	if err != nil {
		return nil, err
	}
	if !strings.HasPrefix(abs, root+string(filepath.Separator)) && abs != root {
		return nil, fmt.Errorf("%s is outside the artwork mirror %s", a.Source, p.root)
	}
	f, err := os.Open(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrNotAvailable, a.Source)
		}
		return nil, err
	}
	return f, nil
}

func (p *local) Check(ctx context.Context) error {
	if _, err := os.ReadDir(p.root); err != nil {
		return fmt.Errorf("artwork mirror %s: %w", p.root, err)
	}
	return nil
}
