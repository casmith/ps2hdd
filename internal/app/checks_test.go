package app

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/model"
)

// Game.Key is prefixed with the platform, so a PS2 title can never match a PS1
// entry. That is why the duplicate check reads only its own library: looking at
// the other one is a comparison that cannot succeed, and on the PS1 side it
// costs a pfsfuse mount of __.POPS to make it.
func TestEnsureNotInstalledIsPerPlatform(t *testing.T) {
	installed := []model.Game{
		{Platform: model.PlatformPS2, Title: "Ico", GameID: "SCUS_971.13"},
	}
	same := model.Game{Platform: model.PlatformPS2, Title: "Ico", GameID: "SCUS-97113"}
	if err := ensureNotInstalledIn(installed, same); !errors.Is(err, ErrAlreadyInstalled) {
		t.Errorf("a title already on the drive was allowed: %v", err)
	}
	// The same serial on the other console is a different game.
	other := model.Game{Platform: model.PlatformPS1, Title: "Ico", GameID: "SCUS_971.13"}
	if err := ensureNotInstalledIn(installed, other); err != nil {
		t.Errorf("a PS1 title was blocked by a PS2 entry: %v", err)
	}
	// And an empty library blocks nothing.
	if err := ensureNotInstalledIn(nil, same); err != nil {
		t.Errorf("an empty library blocked an install: %v", err)
	}
}

// The refusal has to name the title that is already there, which is the only
// way a user can tell which of five hundred lines it is about.
func TestEnsureNotInstalledNamesTheExistingTitle(t *testing.T) {
	installed := []model.Game{{Platform: model.PlatformPS2, Title: "Ico", GameID: "SCUS_971.13"}}
	err := ensureNotInstalledIn(installed, model.Game{Platform: model.PlatformPS2, GameID: "SCUS_971.13"})
	if err == nil {
		t.Fatal("no error")
	}
	if !strings.Contains(err.Error(), "Ico") || !strings.Contains(err.Error(), "SCUS_971.13") {
		t.Errorf("refusal does not name the title: %v", err)
	}
}

// The space check works on a table it was handed rather than one it reads, so
// sharing the read with the duplicate check is structural: this function
// cannot go back to the disk.
func TestEnsurePS2SpaceInWorksOnTheTableGiven(t *testing.T) {
	const mib = int64(1024 * 1024)
	roomy := &apa.TOC{Slices: []apa.Slice{freeSliceFor(512)}}
	if err := ensurePS2SpaceIn(context.Background(), roomy, model.Game{SizeBytes: 400 * mib}); err != nil {
		t.Errorf("a title that fits was refused: %v", err)
	}

	full := &apa.TOC{Slices: []apa.Slice{freeSliceFor(2)}}
	err := ensurePS2SpaceIn(context.Background(), full, model.Game{Title: "Ico", SizeBytes: 4000 * mib})
	var short *InsufficientSpaceError
	if !errors.As(err, &short) {
		t.Fatalf("got %v, want an InsufficientSpaceError", err)
	}
	// It has to name which space ran out: unallocated chunks and room inside
	// __.POPS are different things, fixed in different ways.
	if !strings.Contains(short.Where, "APA") {
		t.Errorf("Where = %q, want the APA table named", short.Where)
	}
	if short.Needed <= short.Free {
		t.Errorf("needed %d is not more than free %d", short.Needed, short.Free)
	}
}

// freeSliceFor builds a slice with every chunk free.
func freeSliceFor(total uint32) apa.Slice {
	return apa.Slice{TotalChunks: total, FreeChunks: total, ChunkMap: make([]bool, total)}
}
