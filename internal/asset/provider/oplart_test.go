package provider

import (
	"context"
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
)

func TestOPLArtLookupBuildsDatabaseURLs(t *testing.T) {
	p, err := newOPLArt(Options{})
	if err != nil {
		t.Fatal(err)
	}
	g := model.Game{Platform: model.PlatformPS2, GameID: "SLUS_207.12"}
	set, err := p.Lookup(context.Background(), g,
		[]model.AssetType{model.AssetCover, model.AssetCoverBack, model.AssetIcon,
			model.AssetSpine, model.AssetBackground, model.AssetScreen, model.AssetScreen2,
			model.AssetConfig})
	if err != nil {
		t.Fatal(err)
	}
	got := map[model.AssetType]string{}
	for _, a := range set.Assets {
		got[a.Type] = a.Source
	}

	const base = oplArtBase + "/PS2/SLUS_207.12/SLUS_207.12_"
	want := map[model.AssetType]string{
		model.AssetCover:     base + "COV.png",
		model.AssetCoverBack: base + "COV2.png",
		model.AssetIcon:      base + "ICO.png",
		model.AssetSpine:     base + "LAB.png",
		// Backgrounds and screenshots are numbered in the database; there is
		// no unnumbered file to fall back on.
		model.AssetBackground: base + "BG_00.png",
		model.AssetScreen:     base + "SCR_00.png",
		model.AssetScreen2:    base + "SCR_01.png",
	}
	for typ, url := range want {
		if got[typ] != url {
			t.Errorf("%s = %q, want %q", typ, got[typ], url)
		}
	}
	// CFG is a settings file, not artwork: no database can generate one.
	if u, ok := got[model.AssetConfig]; ok {
		t.Errorf("CFG was offered a URL: %q", u)
	}
}

func TestOPLArtUsesThePS1Tree(t *testing.T) {
	p, _ := newOPLArt(Options{})
	set, err := p.Lookup(context.Background(),
		model.Game{Platform: model.PlatformPS1, GameID: "SLUS_005.94"},
		[]model.AssetType{model.AssetCover})
	if err != nil {
		t.Fatal(err)
	}
	want := oplArtBase + "/PS1/SLUS_005.94/SLUS_005.94_COV.png"
	if len(set.Assets) != 1 || set.Assets[0].Source != want {
		t.Errorf("got %+v, want %q", set.Assets, want)
	}
}

// The whole point of Supports is that a slot no provider can fill stops being
// reported as a gap the user could close.
func TestSupportsSeparatesUnfillableSlots(t *testing.T) {
	covers, _ := newPS2Covers(Options{})
	want := []model.AssetType{model.AssetCover, model.AssetCoverBack, model.AssetIcon}

	if got := Unsupported(covers, want); len(got) != 2 {
		t.Errorf("ps2-covers unsupported = %v, want COV2 and ICO", got)
	}
	if !Supports(covers, model.AssetCover) {
		t.Error("ps2-covers should supply front covers")
	}

	art, _ := newOPLArt(Options{})
	if got := Unsupported(art, want); len(got) != 0 {
		t.Errorf("opl-art unsupported = %v, want none", got)
	}

	// A chain can fill a slot if any member can.
	chain := Chain{Providers: []Provider{covers, art}}
	if got := Unsupported(chain, want); len(got) != 0 {
		t.Errorf("chain unsupported = %v, want none", got)
	}
}
