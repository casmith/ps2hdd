package asset_test

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/model"
)

// OPL has no PS1 support, so a PS1 title reaches the console as an Apps entry
// -- a renamed POPSTARTER.ELF -- and it looks up an app's artwork by the whole
// boot filename rather than by a serial. appGetItemStartup returns the boot
// value, appGetImage passes it through, and hddGetImage builds
// "<prefix>ART/<value>_<suffix>" (src/appsupport.c, src/hddsupport.c), so the
// file it opens is
//
//	ART/SCUS_941.63.Final Fantasy VII_CD1.ELF_COV.png
//
// Artwork under the serial is where OPL looks for games, and an Apps entry
// never finds it.
func TestAppFilenameIsKeyedOnTheBootName(t *testing.T) {
	boot := "SCUS_941.63.Final Fantasy VII_CD1.ELF"
	if got, want := asset.AppFilename(boot, model.AssetCover), boot+"_COV.png"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
	// The extension is part of the name OPL builds, not stripped from it.
	if got := asset.AppFilename(boot, model.AssetCover); got[:len(boot)] != boot {
		t.Errorf("the boot name was altered: %q", got)
	}
	// Per-game configuration is not artwork and has no Apps-page equivalent.
	if got := asset.AppFilename(boot, model.AssetConfig); got != "" {
		t.Errorf("config produced %q, want no app filename", got)
	}
}

// The launcher is named after the installed VCD, so that is where the boot name
// comes from. A title that is not installed has no VCD name and therefore no
// Apps-page artwork to write yet.
func TestAppBootNameComesFromTheInstalledDisc(t *testing.T) {
	installed := model.Game{
		Platform: model.PlatformPS1,
		Discs: []model.Disc{
			{Number: 2, InstalledName: "SCUS_941.64.Final Fantasy VII_CD2.VCD"},
			{Number: 1, InstalledName: "SCUS_941.63.Final Fantasy VII_CD1.VCD"},
		},
	}
	// Disc 1, because that is the disc the single launcher points at.
	if got, want := asset.AppBootName(installed), "SCUS_941.63.Final Fantasy VII_CD1.ELF"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	for _, g := range []model.Game{
		// A PS2 title is a game, not an app: OPL finds its artwork by serial.
		{Platform: model.PlatformPS2, Discs: []model.Disc{{InstalledName: "PP.SLUS_210.50.Burnout"}}},
		// Not installed yet, so there is no launcher name to key off.
		{Platform: model.PlatformPS1, Discs: []model.Disc{{Number: 1, SourcePath: "/roms/ff7.cue"}}},
		{Platform: model.PlatformPS1},
	} {
		if got := asset.AppBootName(g); got != "" {
			t.Errorf("%s: got %q, want empty", g.Platform, got)
		}
	}
}
