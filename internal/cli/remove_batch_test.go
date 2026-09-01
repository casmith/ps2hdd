package cli

import (
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

func installedEntry(title, id, src string) catalog.CatalogEntry {
	return catalog.CatalogEntry{
		Game: model.Game{
			Platform: model.PlatformPS2, Title: title, GameID: id,
			Installed: true, SizeBytes: 1 << 30, InstallSizeBytes: 1 << 30,
		},
		AvailableInSource: src != "",
		SourceGame:        &model.Game{Platform: model.PlatformPS2, Title: title, GameID: id, SourcePath: src},
	}
}

// The list that installed a set of games has to take them off again, and that
// list is very often a directory listing. An installed title has no source
// path of its own, so a filename only resolves through the catalog -- which is
// what pairs a title on the drive with the archive it came from.
func TestFindSourceFileReachesInstalledTitles(t *testing.T) {
	c := catalog.Catalog{Entries: []catalog.CatalogEntry{
		installedEntry("Ridge Racer V", "SLUS_200.02", "/roms/Ridge Racer V.iso"),
		installedEntry("Ico", "SCUS_971.13", "/roms/Ico (USA).7z"),
	}}
	for _, q := range []string{"Ridge Racer V.iso", "Ico (USA).7z", "Ico (USA)"} {
		m := c.FindSourceFile(q)
		if len(m) != 1 {
			t.Errorf("%q matched %d entries, want 1", q, len(m))
			continue
		}
		if !m[0].Installed {
			t.Errorf("%q resolved to a title reported as not installed", q)
		}
	}
}

// A line naming a title that is not on the drive is a no-op, not a mistake:
// deleting what is already gone changes nothing. A line matching nothing at
// all is a typo, and on a delete list a typo costs more than on an install
// list -- so it stops the run before anything is deleted.
func TestRemoveListDistinguishesAbsentFromUnknown(t *testing.T) {
	c := catalog.Catalog{Entries: []catalog.CatalogEntry{
		installedEntry("Ico", "SCUS_971.13", "/roms/Ico (USA).7z"),
		{
			// In the source directory, never installed.
			Game:              model.Game{Platform: model.PlatformPS2, Title: "Okami", GameID: "SLUS_216.75", SourcePath: "/roms/Okami.7z"},
			AvailableInSource: true,
		},
	}}
	if m := c.FindSourceFile("Okami.7z"); len(m) != 1 || m[0].Installed {
		t.Errorf("a source-only title resolved as installed: %+v", m)
	}
	if m := c.FindSourceFile("Nothing At All.7z"); len(m) != 0 {
		t.Errorf("an unknown filename matched %d entries", len(m))
	}
}

// The plan has to say what it frees, since that is the reason for running it --
// and it is the footprint that comes back, not the size of the source file.
func TestRemovePlanTotalsWhatItFrees(t *testing.T) {
	games := []model.Game{
		{Title: "A", InstallSizeBytes: 1 << 30},
		{Title: "B", InstallSizeBytes: 2 << 30},
		// No footprint recorded: falls back to the file size rather than zero.
		{Title: "C", SizeBytes: 3 << 30},
	}
	if got, want := freedBy(games), int64(6)<<30; got != want {
		t.Errorf("freed = %d, want %d", got, want)
	}
	if got := freedBy(nil); got != 0 {
		t.Errorf("an empty list frees %d", got)
	}
}

// A long title is shortened for the table rather than wrapping it.
func TestPlanTitleIsTruncated(t *testing.T) {
	long := strings.Repeat("x", 60)
	got := components1(long, 44)
	if len([]rune(got)) != 44 {
		t.Errorf("got %d runes, want 44", len([]rune(got)))
	}
	if short := components1("Ico", 44); short != "Ico" {
		t.Errorf("a short title was altered: %q", short)
	}
}
