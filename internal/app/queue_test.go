package app

import (
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
)

// Unpacking is its own state and its own words. Falling through to the default
// reported a title as "installing" while it was in fact decompressing, which
// on a big archive is a minute of a progress bar claiming the wrong thing --
// and indistinguishable from a stall.
func TestExtractingIsItsOwnState(t *testing.T) {
	if got := stateForStage(StageExtracting); got != QueueExtracting {
		t.Errorf("state = %q, want %q", got, QueueExtracting)
	}
	if got := stageText(Progress{Stage: StageExtracting}); got != "Unpacking" {
		t.Errorf("text = %q, want Unpacking", got)
	}
	// It is work in progress, not a finished item: a terminal state would stop
	// the queue reporting anything further about the title.
	if QueueExtracting.Terminal() {
		t.Error("extracting is terminal")
	}
	// Every stage the install path emits has words of its own; the fallback is
	// a raw lowercase constant beside properly written labels.
	for _, st := range []Stage{
		StageValidating, StageInspecting, StageExtracting, StageConverting,
		StageInstalling, StageRemoving, StageVerifying, StageSyncAssets, StageComplete,
	} {
		if got := stageText(Progress{Stage: st}); got == string(st) {
			t.Errorf("stage %q has no label of its own", st)
		}
	}
}

// The worker has to hand the running pass's prefetcher to each install, and
// forgetting to is invisible: every title still installs, just never
// overlapping anything. Exactly the shape of the bug that shipped once.
func TestQueuePassesItsPrefetcherToTheInstall(t *testing.T) {
	q := &Queue{opts: InstallOptions{SyncAssets: true}}
	if got := q.installOpts(); got.Prefetch != nil {
		t.Error("options carried a prefetcher before a pass started")
	}
	pre := &Prefetcher{}
	q.prefetch = pre
	got := q.installOpts()
	if got.Prefetch != pre {
		t.Error("the running pass's prefetcher was not handed to the install")
	}
	// And the rest of the configuration survives.
	if !got.SyncAssets {
		t.Error("the queue's own options were lost")
	}
}

// The worker takes the waiting items in order, which is what the prefetcher is
// given so that it unpacks in the order they will be installed.
func TestWaitingGamesIsWhatIsLeftToDo(t *testing.T) {
	q := &Queue{items: []*QueueItem{
		{Game: model.Game{Title: "done"}, State: QueueComplete},
		{Game: model.Game{Title: "first"}, State: QueueWaiting},
		{Game: model.Game{Title: "busy"}, State: QueueInstalling},
		{Game: model.Game{Title: "second"}, State: QueueWaiting},
	}}
	got := q.waitingGames()
	if len(got) != 2 || got[0].Title != "first" || got[1].Title != "second" {
		t.Fatalf("got %+v, want the two waiting titles in order", got)
	}
}

// Titles queued while a pass is running have to reach the prefetcher, or
// anything added behind the current work quietly stops being unpacked ahead.
func TestQueueAddFeedsARunningPrefetcher(t *testing.T) {
	pre := &Prefetcher{
		expect:  map[string]bool{},
		ready:   map[string]*PrefetchedSource{},
		waiters: map[string]chan struct{}{},
		done:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	q := &Queue{nextID: 1, prefetch: pre}
	g := model.Game{Title: "Ico", SourcePath: "/roms/Ico.7z", ArchiveMember: "Ico.iso"}
	q.Add(g)

	pre.mu.Lock()
	pending := len(pre.pending)
	pre.mu.Unlock()
	if pending != 1 {
		t.Errorf("the prefetcher was given %d titles, want 1", pending)
	}
}
