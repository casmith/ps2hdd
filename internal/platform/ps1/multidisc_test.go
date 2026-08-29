package ps1_test

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

func TestDiscNumber(t *testing.T) {
	cases := map[string]int{
		"Final Fantasy VII (Disc 1).cue":          1,
		"Final Fantasy VII (Disc 2).cue":          2,
		"Metal Gear Solid [CD2].cue":              2,
		"Chrono Cross - Disc 1 of 2.cue":          1,
		"Parasite Eve II (USA) (Disk 2).cue":      2,
		"Disc 3.cue":                              3,
		"Castlevania - Symphony of the Night.cue": 0,
		"game.cue": 0,
	}
	for in, want := range cases {
		if got := ps1.DiscNumber(in); got != want {
			t.Errorf("DiscNumber(%q) = %d, want %d", in, got, want)
		}
	}
}

func TestBaseTitle(t *testing.T) {
	cases := map[string]string{
		"Final Fantasy VII (USA) (Disc 1).cue":    "Final Fantasy VII",
		"Final Fantasy VII (USA) (Disc 3).cue":    "Final Fantasy VII",
		"Metal Gear Solid [CD2].cue":              "Metal Gear Solid",
		"Castlevania - Symphony of the Night.cue": "Castlevania - Symphony of the Night",
		"Disc 1.cue": "",
	}
	for in, want := range cases {
		if got := ps1.BaseTitle(in); got != want {
			t.Errorf("BaseTitle(%q) = %q, want %q", in, got, want)
		}
	}
}

// A per-title directory holding "Disc 1.cue", "Disc 2.cue" is one of the two
// layouts users actually have; the directory name is the title.
func TestGroupPerTitleDirectory(t *testing.T) {
	discs := []ps1.Disc{
		{CuePath: "/psx/Final Fantasy VII/Disc 1.cue", GameID: "SCUS_941.63", SizeBytes: 100, DiscNumber: 1},
		{CuePath: "/psx/Final Fantasy VII/Disc 2.cue", GameID: "SCUS_941.64", SizeBytes: 200, DiscNumber: 2},
		{CuePath: "/psx/Final Fantasy VII/Disc 3.cue", GameID: "SCUS_941.65", SizeBytes: 300, DiscNumber: 3},
	}
	games := ps1.Group(discs, "/psx")
	if len(games) != 1 {
		t.Fatalf("got %d games, want 1", len(games))
	}
	g := games[0]
	if g.Title != "Final Fantasy VII" {
		t.Errorf("Title = %q", g.Title)
	}
	if g.DiscCount() != 3 || !g.IsMultiDisc() {
		t.Errorf("DiscCount = %d", g.DiscCount())
	}
	// The title's identity is the first disc's serial; the other discs keep
	// their own, which differ on a real release.
	if g.GameID != "SCUS_941.63" {
		t.Errorf("GameID = %q", g.GameID)
	}
	if g.Discs[1].GameID != "SCUS_941.64" {
		t.Errorf("disc 2 GameID = %q; discs must keep their own serials", g.Discs[1].GameID)
	}
	if g.SizeBytes != 600 {
		t.Errorf("SizeBytes = %d, want the sum of all discs", g.SizeBytes)
	}
}

// The other common layout is a flat directory with disc tags in the filename.
func TestGroupFlatDirectory(t *testing.T) {
	discs := []ps1.Disc{
		{CuePath: "/psx/Metal Gear Solid (Disc 1).cue", GameID: "SLUS_005.94", DiscNumber: 1},
		{CuePath: "/psx/Metal Gear Solid (Disc 2).cue", GameID: "SLUS_007.76", DiscNumber: 2},
		{CuePath: "/psx/Castlevania - Symphony of the Night.cue", GameID: "SLUS_000.67"},
	}
	games := ps1.Group(discs, "/psx")
	if len(games) != 2 {
		t.Fatalf("got %d games, want 2", len(games))
	}
	byTitle := map[string]int{}
	for _, g := range games {
		byTitle[g.Title] = g.DiscCount()
	}
	if byTitle["Metal Gear Solid"] != 2 {
		t.Errorf("Metal Gear Solid has %d discs", byTitle["Metal Gear Solid"])
	}
	if byTitle["Castlevania - Symphony of the Night"] != 1 {
		t.Errorf("single-disc title grouped wrongly: %v", byTitle)
	}
}

// Two different titles that happen to sit in the same directory must not be
// merged, and one title split across directories must not be merged either:
// grouping is per directory, then per base title.
func TestGroupDoesNotMergeUnrelatedTitles(t *testing.T) {
	discs := []ps1.Disc{
		{CuePath: "/psx/Tomb Raider (Disc 1).cue", GameID: "SLUS_001.52", DiscNumber: 1},
		{CuePath: "/psx/Tekken 3.cue", GameID: "SCUS_942.64"},
		{CuePath: "/psx/other/Tekken 3.cue", GameID: "SCES_016.24"},
	}
	games := ps1.Group(discs, "/psx")
	if len(games) != 3 {
		t.Fatalf("got %d games, want 3: %+v", len(games), games)
	}
}

// Discs are ordered by disc number regardless of the order they were scanned.
func TestGroupOrdersDiscs(t *testing.T) {
	discs := []ps1.Disc{
		{CuePath: "/psx/FF7/Disc 3.cue", GameID: "C", DiscNumber: 3},
		{CuePath: "/psx/FF7/Disc 1.cue", GameID: "A", DiscNumber: 1},
		{CuePath: "/psx/FF7/Disc 2.cue", GameID: "B", DiscNumber: 2},
	}
	g := ps1.Group(discs, "/psx")[0]
	for i, d := range g.Discs {
		if d.Number != i+1 {
			t.Errorf("disc %d has number %d", i, d.Number)
		}
	}
	if g.GameID != "A" {
		t.Errorf("identity = %q, want the first disc's serial", g.GameID)
	}
}

// When the user names several cuesheets on one command line they are one
// title by definition, whatever the filenames say.
func TestGroupExplicit(t *testing.T) {
	discs := []ps1.Disc{
		{CuePath: "/downloads/mgs-cd2.cue", GameID: "SLUS_007.76"},
		{CuePath: "/downloads/mgs-cd1.cue", GameID: "SLUS_005.94"},
	}
	g := ps1.GroupExplicit(discs, "Metal Gear Solid")
	if g.Title != "Metal Gear Solid" || g.DiscCount() != 2 {
		t.Fatalf("game = %+v", g)
	}
	// Untagged discs are numbered in the order the user gave them.
	if g.Discs[0].Number != 1 || g.Discs[1].Number != 2 {
		t.Errorf("disc numbers = %d,%d", g.Discs[0].Number, g.Discs[1].Number)
	}
	if g.Discs[0].SourcePath != "/downloads/mgs-cd2.cue" {
		t.Errorf("order changed: %q", g.Discs[0].SourcePath)
	}
	if g.GameID != "SLUS_007.76" {
		t.Errorf("identity = %q, want the first-given disc serial", g.GameID)
	}
}

func TestGroupExplicitHonoursDiscTags(t *testing.T) {
	discs := []ps1.Disc{
		{CuePath: "/d/Game (Disc 2).cue", GameID: "B", DiscNumber: 2},
		{CuePath: "/d/Game (Disc 1).cue", GameID: "A", DiscNumber: 1},
	}
	g := ps1.GroupExplicit(discs, "")
	if g.Title != "Game" {
		t.Errorf("Title = %q", g.Title)
	}
	if g.Discs[0].Number != 1 || g.Discs[0].GameID != "A" {
		t.Errorf("tagged discs were not reordered: %+v", g.Discs)
	}
}

func TestGroupExplicitEmpty(t *testing.T) {
	g := ps1.GroupExplicit(nil, "")
	if g.DiscCount() != 1 || len(g.Discs) != 0 {
		t.Errorf("empty group = %+v", g)
	}
}
