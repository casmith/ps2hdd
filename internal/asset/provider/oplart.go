package provider

import (
	"context"
	"fmt"
	"io"

	"github.com/casmith/ps2hdd/internal/model"
)

// oplArtBase is the OPL Manager GameArt Database, mirrored on GitHub and
// reachable over plain HTTPS with no authentication.
//
// Layout is <platform>/<opl serial>/<opl serial>_<slot>.png, keyed by the OPL
// serial form ps2hdd already uses everywhere else.
const oplArtBase = "https://raw.githubusercontent.com/Luden02/psx-ps2-opl-art-database/main"

// oplArtSlots maps an asset type to the filename suffix the database uses.
//
// Most slots are named for the type. Backgrounds and screenshots are not:
// the database keeps every one a game has, numbered from 00, and there is no
// unnumbered file to fall back on. OPL has room for one background and two
// screenshots, so the first of each is what gets installed.
var oplArtSlots = map[model.AssetType]string{
	model.AssetCover:      "COV",
	model.AssetCoverBack:  "COV2",
	model.AssetSpine:      "LAB",
	model.AssetIcon:       "ICO",
	model.AssetLogo:       "LGO",
	model.AssetBackground: "BG_00",
	model.AssetScreen:     "SCR_00",
	model.AssetScreen2:    "SCR_01",
}

// oplArt serves the whole OPL art set: front and back covers, the disc image,
// the spine, the logo, backgrounds and screenshots.
//
// It is preferred over the cover-only databases for two reasons beyond
// coverage. Its images are PNG, which is what OPL's own guidelines specify and
// what the destination filename claims; and they are already at OPL's exact
// pixel sizes, so nothing has to be scaled on a console that would rather not.
type oplArt struct {
	client Doer
}

func newOPLArt(o Options) (Provider, error) {
	c := o.HTTP
	if c == nil {
		c = defaultClient()
	}
	return &oplArt{client: c}, nil
}

func (p *oplArt) Name() string { return "opl-art" }

// Supports is every image slot OPL has. CFG is not among them: it is a
// settings file, not artwork, and no database can generate one.
func (p *oplArt) Supports() []model.AssetType {
	out := make([]model.AssetType, 0, len(oplArtSlots))
	for _, t := range model.ArtTypes {
		if _, ok := oplArtSlots[t]; ok {
			out = append(out, t)
		}
	}
	return out
}

func (p *oplArt) Lookup(ctx context.Context, game model.Game, want []model.AssetType) (model.AssetSet, error) {
	var set model.AssetSet
	id := model.OPLGameID(game.GameID)
	if model.NormalizeGameID(game.GameID) == "" {
		return set, nil
	}
	dir := "PS2"
	if game.Platform == model.PlatformPS1 {
		dir = "PS1"
	}
	for _, t := range want {
		slot, ok := oplArtSlots[t]
		if !ok {
			continue
		}
		set.Assets = append(set.Assets, model.Asset{
			Type:     t,
			GameID:   game.GameID,
			Platform: game.Platform,
			Source:   fmt.Sprintf("%s/%s/%s/%s_%s.png", oplArtBase, dir, id, id, slot),
		})
	}
	return set, nil
}

func (p *oplArt) Fetch(ctx context.Context, a model.Asset) (io.ReadCloser, error) {
	return httpFetch(ctx, p.client, a.Source)
}

func (p *oplArt) Check(ctx context.Context) error {
	return httpReachable(ctx, p.client, "https://raw.githubusercontent.com/")
}
