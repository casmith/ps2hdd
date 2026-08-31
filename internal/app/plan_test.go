package app

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
)

func entries(sizes ...int64) []PlanEntry {
	out := make([]PlanEntry, 0, len(sizes))
	for i, n := range sizes {
		out = append(out, PlanEntry{
			Game:  model.Game{Title: string(rune('A' + i))},
			Bytes: n,
			Fits:  n > 0,
		})
	}
	return out
}

func TestPlatformPlanTotals(t *testing.T) {
	p := PlatformPlan{Entries: []PlanEntry{
		{Bytes: 100, Fits: true},
		{Bytes: 200, Fits: true},
		{Bytes: 400, Fits: false},
		{Bytes: 800, Fits: false},
	}}
	if got := p.Count(); got != 4 {
		t.Errorf("Count = %d", got)
	}
	if got := p.Bytes(); got != 1500 {
		t.Errorf("Bytes = %d, want the whole plan including what does not fit", got)
	}
	if n, b := p.Fitting(); n != 2 || b != 300 {
		t.Errorf("Fitting = %d, %d; want 2, 300", n, b)
	}
	if n, b := p.Excess(); n != 2 || b != 1200 {
		t.Errorf("Excess = %d, %d; want 2, 1200", n, b)
	}
}

// The two platforms consume different pools, so a plan only fits when both
// halves do. A PS2 library that fits says nothing about whether the PS1 one
// will go into __.POPS.
func TestInstallPlanFitsNeedsBothHalves(t *testing.T) {
	ok := PlanEntry{Bytes: 1, Fits: true}
	no := PlanEntry{Bytes: 1, Fits: false}
	cases := map[string]struct {
		ps2, ps1 []PlanEntry
		want     bool
	}{
		"both fit":        {[]PlanEntry{ok}, []PlanEntry{ok}, true},
		"ps2 short":       {[]PlanEntry{no}, []PlanEntry{ok}, false},
		"ps1 short":       {[]PlanEntry{ok}, []PlanEntry{no}, false},
		"nothing to do":   {nil, nil, true},
		"only ps1, short": {nil, []PlanEntry{no}, false},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			p := InstallPlan{
				PS2: PlatformPlan{Entries: tc.ps2},
				PS1: PlatformPlan{Entries: tc.ps1},
			}
			if got := p.Fits(); got != tc.want {
				t.Errorf("Fits = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPlanOptionsFilter(t *testing.T) {
	// Neither named means both, which is what a bare --all asks for.
	both := PlanOptions{}
	if !both.wants(model.PlatformPS2) || !both.wants(model.PlatformPS1) {
		t.Error("an unfiltered plan excluded a platform")
	}
	only1 := PlanOptions{PS1: true}
	if only1.wants(model.PlatformPS2) || !only1.wants(model.PlatformPS1) {
		t.Error("--ps1 did not select PS1 alone")
	}
	only2 := PlanOptions{PS2: true}
	if !only2.wants(model.PlatformPS2) || only2.wants(model.PlatformPS1) {
		t.Error("--ps2 did not select PS2 alone")
	}
	// Both named is the same as neither, rather than nothing.
	all := PlanOptions{PS1: true, PS2: true}
	if !all.wants(model.PlatformPS2) || !all.wants(model.PlatformPS1) {
		t.Error("--ps1 --ps2 together excluded a platform")
	}
}

// Space that was never measured must not be reported as space that is free.
// FreeKnown is what separates "it fits" from "nothing said otherwise".
func TestUnmeasuredSpaceIsNotAVerdict(t *testing.T) {
	p := PlatformPlan{FreeKnown: false, Entries: entries(100, 200)}
	if n, _ := p.Excess(); n != 0 {
		t.Error("titles were refused against a free figure that was never taken")
	}
	if p.FreeBytes != 0 {
		t.Error("an unmeasured plan carried a free figure")
	}
}

// PS1 titles are files in an already-allocated partition, so there is no chunk
// arithmetic -- but the running total matters just as much. Checking each
// against the partition's full free space would report a library twice the
// size of __.POPS as fitting, one title at a time.
func TestFitSequentiallyConsumesTheRoom(t *testing.T) {
	games := []model.Game{
		{Title: "A", InstallSizeBytes: 400},
		{Title: "B", InstallSizeBytes: 400},
		{Title: "C", InstallSizeBytes: 400},
	}
	got := fitSequentially(games, 1000, true)
	for i, want := range []bool{true, true, false} {
		if got[i].Fits != want {
			t.Errorf("%s: fits = %v, want %v", got[i].Game.Title, got[i].Fits, want)
		}
	}
	// A title that did not fit must not have taken the room from the next one,
	// which could be small enough to still go on.
	got = fitSequentially(append(games, model.Game{Title: "D", InstallSizeBytes: 100}), 1000, true)
	if !got[3].Fits {
		t.Error("a small title after an oversized one was refused the room it had")
	}
	// Every title still carries its own size, fitting or not, so the report
	// can say how much short the plan is.
	for _, e := range got {
		if e.Bytes == 0 {
			t.Errorf("%s has no size", e.Game.Title)
		}
	}
}

// Space that was never measured is not space that is full.
func TestFitSequentiallyWithNothingMeasured(t *testing.T) {
	games := []model.Game{{InstallSizeBytes: 1 << 40}, {InstallSizeBytes: 1 << 40}}
	for _, e := range fitSequentially(games, 0, false) {
		if !e.Fits {
			t.Error("a title was refused against a free figure that was never taken")
		}
	}
}
