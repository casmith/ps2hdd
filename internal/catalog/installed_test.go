package catalog_test

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

// The mapping from hdl_dump's descriptors to catalog entries is shared with the
// install path, which reads the partition table once and uses it for both of
// its pre-write checks. Having the mapping in one place is what stops that path
// growing a second, subtly different version -- the first draft of it dropped
// this fallback, which would have left the "already installed" refusal naming
// no title at all.
func TestPS2GamesFromFallsBackToTheSerial(t *testing.T) {
	got := catalog.PS2GamesFrom([]apa.GameInfo{
		{Name: "Ico", Startup: "SCUS-97113"},
		{Name: "", Startup: "SLUS-20212"},
	})
	if len(got) != 2 {
		t.Fatalf("got %d games, want 2", len(got))
	}
	byID := map[string]model.Game{}
	for _, g := range got {
		byID[g.GameID] = g
	}
	if g := byID["SCUS_971.13"]; g.Title != "Ico" {
		t.Errorf("title = %q, want Ico", g.Title)
	}
	// A partition with no name is named by its serial rather than by nothing.
	if g := byID["SLUS_202.12"]; g.Title != "SLUS_202.12" {
		t.Errorf("untitled game = %q, want its serial", g.Title)
	}
	for _, g := range got {
		if g.Platform != model.PlatformPS2 || !g.Installed {
			t.Errorf("%s: platform=%q installed=%v", g.GameID, g.Platform, g.Installed)
		}
	}
}
