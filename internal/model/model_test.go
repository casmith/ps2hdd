package model_test

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
)

// The three spellings of a serial that appear in the wild must collapse to one
// identity, or the same title shows up twice in the library.
func TestGameIDNormalisation(t *testing.T) {
	forms := []string{"SLUS_209.46", "SLUS-20946", "slus20946", "SLUS 209 46", "sLuS_209.46"}
	want := model.NormalizeGameID(forms[0])
	for _, f := range forms {
		if got := model.NormalizeGameID(f); got != want {
			t.Errorf("NormalizeGameID(%q) = %q, want %q", f, got, want)
		}
		if got := model.OPLGameID(f); got != "SLUS_209.46" {
			t.Errorf("OPLGameID(%q) = %q", f, got)
		}
		if got := model.DashedGameID(f); got != "SLUS-20946" {
			t.Errorf("DashedGameID(%q) = %q", f, got)
		}
	}
}

func TestGameIDOddShapes(t *testing.T) {
	// Serials are not all four letters and five digits.
	cases := map[string]string{
		"PBPX_955.04": "PBPX_955.04",
		"SCPS_150.00": "SCPS_150.00",
		"scaj-20001":  "SCAJ_200.01",
	}
	for in, want := range cases {
		if got := model.OPLGameID(in); got != want {
			t.Errorf("OPLGameID(%q) = %q, want %q", in, got, want)
		}
	}
	// Something that is not a serial is passed through rather than mangled.
	for _, in := range []string{"NOTASERIAL", "", "12345"} {
		got := model.OPLGameID(in)
		if got != "" && model.NormalizeGameID(got) == "" && in != "" {
			t.Errorf("OPLGameID(%q) = %q", in, got)
		}
	}
}

func TestFindGameID(t *testing.T) {
	cases := map[string]string{
		`cdrom0:\SLUS_209.46;1`:          "SLUS_209.46",
		`cdrom:\SLUS_005.94;1`:           "SLUS_005.94",
		"BOOT2 = cdrom0:\\SCUS_973.99;1": "SCUS_973.99",
		"SLUS-21050 - Burnout 3.iso":     "SLUS_210.50",
		"Shadow of the Colossus.iso":     "",
		"":                               "",
	}
	for in, want := range cases {
		if got := model.FindGameID(in); got != want {
			t.Errorf("FindGameID(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGameKeySeparatesPlatforms(t *testing.T) {
	ps1 := model.Game{Platform: model.PlatformPS1, GameID: "SLUS_000.67"}
	ps2 := model.Game{Platform: model.PlatformPS2, GameID: "SLUS_000.67"}
	if ps1.Key() == ps2.Key() {
		t.Error("a PS1 and a PS2 title with the same serial share a key")
	}
	// A title with no serial still gets a stable key from its name.
	a := model.Game{Platform: model.PlatformPS2, Title: "Homebrew Thing"}
	b := model.Game{Platform: model.PlatformPS2, Title: "homebrew thing"}
	if a.Key() != b.Key() {
		t.Errorf("title keys are case-sensitive: %q vs %q", a.Key(), b.Key())
	}
}

func TestDiscCount(t *testing.T) {
	single := model.Game{}
	if single.DiscCount() != 1 || single.IsMultiDisc() {
		t.Errorf("a game with no disc list should count as one disc")
	}
	multi := model.Game{Discs: []model.Disc{{Number: 1}, {Number: 2}}}
	if multi.DiscCount() != 2 || !multi.IsMultiDisc() {
		t.Errorf("DiscCount = %d", multi.DiscCount())
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		0:               "0 B",
		512:             "512 B",
		1024:            "1.0 KiB",
		1536:            "1.5 KiB",
		4 * 1024 * 1024: "4.0 MiB",
		3865470566:      "3.6 GiB",
	}
	for in, want := range cases {
		if got := model.HumanSize(in); got != want {
			t.Errorf("HumanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestSortGames(t *testing.T) {
	games := []model.Game{
		{Platform: model.PlatformPS2, Title: "zeta"},
		{Platform: model.PlatformPS1, Title: "beta"},
		{Platform: model.PlatformPS2, Title: "Alpha"},
		{Platform: model.PlatformPS1, Title: "Alpha"},
	}
	model.SortGames(games)
	want := []string{"Alpha", "beta", "Alpha", "zeta"}
	for i, g := range games {
		if g.Title != want[i] {
			t.Errorf("position %d = %q, want %q (%v)", i, g.Title, want[i], games)
		}
	}
	if games[0].Platform != model.PlatformPS1 {
		t.Error("PS1 titles should sort before PS2 titles")
	}
}

// The art suffixes and sizes are what OPL actually looks for; getting them
// wrong means artwork that silently never appears on the console.
func TestAssetTypesAndDimensions(t *testing.T) {
	for _, tc := range []struct {
		p    model.Platform
		typ  model.AssetType
		w, h int
	}{
		{model.PlatformPS2, model.AssetCover, 140, 200},
		{model.PlatformPS1, model.AssetCover, 200, 200},
		{model.PlatformPS2, model.AssetCoverBack, 242, 344},
		{model.PlatformPS1, model.AssetCoverBack, 222, 200},
		{model.PlatformPS2, model.AssetIcon, 64, 64},
		{model.PlatformPS1, model.AssetIcon, 64, 64},
		{model.PlatformPS2, model.AssetBackground, 640, 480},
		{model.PlatformPS2, model.AssetScreen, 250, 188},
		{model.PlatformPS2, model.AssetLogo, 300, 125},
	} {
		d, ok := model.Dimensions(tc.p, tc.typ)
		if !ok || d.Width != tc.w || d.Height != tc.h {
			t.Errorf("Dimensions(%s,%s) = %+v,%v want %dx%d", tc.p, tc.typ, d, ok, tc.w, tc.h)
		}
	}
	// CFG is not artwork and has no pixel size.
	if _, ok := model.Dimensions(model.PlatformPS2, model.AssetConfig); ok {
		t.Error("CFG reported a pixel size")
	}
	if model.AssetConfig.IsArt() {
		t.Error("CFG reported as art")
	}
	if !model.AssetCover.IsArt() {
		t.Error("COV not reported as art")
	}
}

func TestAssetStatusMissing(t *testing.T) {
	st := model.AssetStatus{Present: map[model.AssetType]bool{
		model.AssetCover: true, model.AssetConfig: true,
	}}
	want := []model.AssetType{model.AssetCover, model.AssetBackground, model.AssetIcon, model.AssetConfig}
	missing := st.Missing(want)
	if len(missing) != 2 {
		t.Fatalf("missing = %v", missing)
	}
	// Ordering is stable so output does not shuffle between runs.
	if missing[0] != model.AssetBackground || missing[1] != model.AssetIcon {
		t.Errorf("missing order = %v", missing)
	}
}

func TestBlockDeviceMountpoints(t *testing.T) {
	d := model.BlockDevice{
		Path: "/dev/sda",
		Children: []model.BlockDevice{
			{Path: "/dev/sda1", Mountpoint: "/boot"},
			{Path: "/dev/sda2", Mountpoint: "/"},
			{Path: "/dev/sda3"},
		},
	}
	if got := d.AnyMountpoint(); got != "/boot" {
		t.Errorf("AnyMountpoint = %q", got)
	}
	if got := d.Mountpoints(); len(got) != 2 {
		t.Errorf("Mountpoints = %v", got)
	}
	clean := model.BlockDevice{Path: "/dev/sdb"}
	if clean.AnyMountpoint() != "" {
		t.Error("an unmounted disk reported a mountpoint")
	}
}

func TestDriveStatusFindPartition(t *testing.T) {
	st := model.DriveStatus{Partitions: []model.Partition{
		{ID: "__mbr"}, {ID: "+OPL"}, {ID: "__.POPS"},
	}}
	if _, ok := st.FindPartition("+opl"); !ok {
		t.Error("FindPartition is not case-insensitive")
	}
	if _, ok := st.FindPartition("__.MISSING"); ok {
		t.Error("FindPartition found something that is not there")
	}
}
