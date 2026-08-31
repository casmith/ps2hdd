package cli

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/model"
)

func planEntry(title string, fits bool) app.PlanEntry {
	return app.PlanEntry{Game: model.Game{Title: title}, Fits: fits}
}

// A title the plan could not fit is named, not tried. Attempting it anyway
// would rediscover what the plan already worked out -- one refusal at a time,
// and only after everything before it had finished.
func TestSplitPlanLeavesWhatWillNotFit(t *testing.T) {
	plan := app.InstallPlan{
		PS2: app.PlatformPlan{Entries: []app.PlanEntry{
			planEntry("Ico", true),
			planEntry("Gran Turismo 4", false),
		}},
		PS1: app.PlatformPlan{Entries: []app.PlanEntry{
			planEntry("Metal Gear Solid", true),
			planEntry("Final Fantasy VII", false),
		}},
	}
	todo, skipped := splitPlan(plan)

	if len(todo) != 2 {
		t.Fatalf("got %d to install, want 2: %+v", len(todo), todo)
	}
	// Both platforms are installed in one run, PS2 first as the plan lists them.
	if todo[0].Title != "Ico" || todo[1].Title != "Metal Gear Solid" {
		t.Errorf("todo = %q, %q", todo[0].Title, todo[1].Title)
	}
	if len(skipped) != 2 {
		t.Fatalf("got %d not attempted, want 2: %v", len(skipped), skipped)
	}
	for i, want := range []string{"Gran Turismo 4", "Final Fantasy VII"} {
		if skipped[i] != want {
			t.Errorf("not attempted %d = %q, want %q", i, skipped[i], want)
		}
	}
}

// A plan where nothing fits installs nothing rather than trying everything.
func TestSplitPlanWithNoRoom(t *testing.T) {
	plan := app.InstallPlan{PS2: app.PlatformPlan{Entries: []app.PlanEntry{
		planEntry("A", false), planEntry("B", false),
	}}}
	todo, skipped := splitPlan(plan)
	if len(todo) != 0 {
		t.Errorf("got %d to install on a full drive, want none", len(todo))
	}
	if len(skipped) != 2 {
		t.Errorf("got %d not attempted, want 2", len(skipped))
	}
}

func TestSplitPlanEmpty(t *testing.T) {
	todo, skipped := splitPlan(app.InstallPlan{})
	if len(todo) != 0 || len(skipped) != 0 {
		t.Errorf("an empty plan produced %d/%d", len(todo), len(skipped))
	}
}
