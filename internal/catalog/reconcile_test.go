package catalog_test

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

func installedPS2(title, id string) model.Game {
	return model.Game{
		Platform: model.PlatformPS2, Title: title, GameID: id,
		Installed: true, StorageBackend: model.BackendHDL,
		PartitionName: "PP." + id + "." + title,
	}
}

func sourcePS2(title, id, path string) model.Game {
	return model.Game{Platform: model.PlatformPS2, Title: title, GameID: id, SourcePath: path}
}

func TestReconcileMatchesAcrossIDFormats(t *testing.T) {
	// The HDD records SLUS_210.50; a source file may be named SLUS-21050.
	// They are one title, not two rows.
	c := catalog.Reconcile(
		[]model.Game{installedPS2("Burnout 3", "SLUS_210.50")},
		[]model.Game{sourcePS2("Burnout 3 Takedown", "SLUS-21050", "/games/b3.iso")},
		nil,
	)
	if len(c.Entries) != 1 {
		t.Fatalf("got %d entries, want 1: %+v", len(c.Entries), c.Entries)
	}
	e := c.Entries[0]
	if !e.Installed || !e.AvailableInSource {
		t.Errorf("entry = installed:%v source:%v", e.Installed, e.AvailableInSource)
	}
	if e.State() != catalog.StateInstalledAndSource {
		t.Errorf("State = %q", e.State())
	}
	if e.SourcePath != "/games/b3.iso" {
		t.Errorf("SourcePath = %q; the installed row should learn where the image is", e.SourcePath)
	}
	if e.SourceGame == nil || e.SourceGame.Title != "Burnout 3 Takedown" {
		t.Errorf("SourceGame = %+v", e.SourceGame)
	}
	// The installed record wins on title: it is what the console shows.
	if e.Title != "Burnout 3" {
		t.Errorf("Title = %q, want the installed name", e.Title)
	}
}

func TestReconcileKeepsSourceOnlyAndInstalledOnly(t *testing.T) {
	c := catalog.Reconcile(
		[]model.Game{installedPS2("God Hand", "SLUS_215.03")},
		[]model.Game{sourcePS2("Gran Turismo 4", "SCUS_973.28", "/games/gt4.iso")},
		nil,
	)
	if len(c.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(c.Entries))
	}
	states := map[string]catalog.State{}
	for _, e := range c.Entries {
		states[e.Title] = e.State()
	}
	if states["God Hand"] != catalog.StateInstalled {
		t.Errorf("God Hand = %q", states["God Hand"])
	}
	if states["Gran Turismo 4"] != catalog.StateAvailable {
		t.Errorf("Gran Turismo 4 = %q", states["Gran Turismo 4"])
	}
}

// A source directory is never evidence that something is installed.
func TestReconcileNeverInfersInstalledFromSource(t *testing.T) {
	c := catalog.Reconcile(nil, []model.Game{sourcePS2("X", "SLUS_200.01", "/g/x.iso")}, nil)
	if c.Entries[0].Installed {
		t.Fatal("a source-only title was reported as installed")
	}
}

// PS1 and PS2 titles never collide even if a serial somehow matched.
func TestReconcileSeparatesPlatforms(t *testing.T) {
	c := catalog.Reconcile(
		[]model.Game{{Platform: model.PlatformPS1, Title: "G", GameID: "SLUS_000.67", Installed: true}},
		[]model.Game{{Platform: model.PlatformPS2, Title: "G", GameID: "SLUS_000.67"}},
		nil,
	)
	if len(c.Entries) != 2 {
		t.Fatalf("got %d entries, want 2", len(c.Entries))
	}
}

func TestFilter(t *testing.T) {
	c := catalog.Reconcile(
		[]model.Game{
			installedPS2("Burnout 3", "SLUS_210.50"),
			{Platform: model.PlatformPS1, Title: "Final Fantasy VII", GameID: "SCUS_941.63",
				Installed: true, Discs: []model.Disc{{Number: 1}, {Number: 2}, {Number: 3}}},
		},
		[]model.Game{sourcePS2("God Hand", "SLUS_215.03", "/g/gh.iso")},
		nil,
	)
	c.Entries[0].MissingAssets = []model.AssetType{model.AssetCover}

	cases := []struct {
		name string
		f    catalog.Filter
		want int
	}{
		{"all", catalog.Filter{}, 3},
		{"ps2", catalog.Filter{Platform: model.PlatformPS2}, 2},
		{"ps1", catalog.Filter{Platform: model.PlatformPS1}, 1},
		{"installed", catalog.Filter{Installed: true}, 2},
		{"not installed", catalog.Filter{NotInstalled: true}, 1},
		{"multi disc", catalog.Filter{MultiDisc: true}, 1},
		{"search title", catalog.Filter{Search: "hand"}, 1},
		{"search serial", catalog.Filter{Search: "SLUS_210.50"}, 1},
		{"search serial loose", catalog.Filter{Search: "slus21050"}, 1},
		{"search miss", catalog.Filter{Search: "nothing here"}, 0},
	}
	for _, tc := range cases {
		if got := len(c.Apply(tc.f)); got != tc.want {
			t.Errorf("%s: %d entries, want %d", tc.name, got, tc.want)
		}
	}

	// MissingAsset depends on the asset pass having run.
	if got := len(c.Apply(catalog.Filter{MissingAsset: true})); got != 1 {
		t.Errorf("missing-asset filter matched %d", got)
	}
}

func TestFindIsExactBeforeFuzzy(t *testing.T) {
	c := catalog.Reconcile([]model.Game{
		installedPS2("Final Fantasy X", "SCUS_971.72"),
		installedPS2("Final Fantasy X-2", "SCUS_973.04"),
	}, nil, nil)

	// A serial is unambiguous.
	if m := c.Find("SCUS_971.72"); len(m) != 1 || m[0].Title != "Final Fantasy X" {
		t.Errorf("serial lookup = %+v", m)
	}
	if m := c.Find("scus97172"); len(m) != 1 {
		t.Errorf("loose serial lookup = %+v", m)
	}
	// An exact title beats the substring that also matches the other row.
	if m := c.Find("Final Fantasy X"); len(m) != 1 || m[0].GameID != "SCUS_971.72" {
		t.Errorf("exact title lookup = %+v", m)
	}
	// A partial title that matches two rows must report both, so the caller
	// can refuse rather than delete the wrong game.
	if m := c.Find("Final Fantasy"); len(m) != 2 {
		t.Errorf("ambiguous lookup = %+v, want both rows", m)
	}
	if m := c.Find("nothing"); len(m) != 0 {
		t.Errorf("miss = %+v", m)
	}
}

func TestCounts(t *testing.T) {
	c := catalog.Reconcile(
		[]model.Game{installedPS2("A", "SLUS_200.01"), installedPS2("B", "SLUS_200.02")},
		[]model.Game{sourcePS2("C", "SLUS_200.03", "/g/c.iso")},
		nil,
	)
	c.Entries[0].MissingAssets = []model.AssetType{model.AssetCover}
	inst, avail, missing := c.Counts()
	if inst != 2 || avail != 1 || missing != 1 {
		t.Errorf("counts = %d/%d/%d", inst, avail, missing)
	}
}
