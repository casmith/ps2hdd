package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/casmith/ps2hdd/internal/model"
)

// newTestPrefetcher builds one already given the titles, by the same route a
// caller uses -- pre-seeding the fields by hand once left Add treating them as
// already queued, so the loop had nothing to take and waited forever.
func newTestPrefetcher(games ...model.Game) *Prefetcher {
	p := &Prefetcher{
		expect:  map[string]bool{},
		ready:   map[string]*PrefetchedSource{},
		waiters: map[string]chan struct{}{},
		done:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	p.Add(games...)
	return p
}

func archived(title string) model.Game {
	return model.Game{Title: title, SourcePath: "/roms/" + title + ".7z", ArchiveMember: title + ".iso"}
}

// A nil prefetcher is the ordinary single-install case, and every call site
// treats it as "extract inline". Making that the zero value is what keeps the
// pipelined and plain paths one code path rather than two.
func TestNilPrefetcherIsInline(t *testing.T) {
	var p *Prefetcher
	if src, ok := p.Take(context.Background(), archived("Ico")); ok || src != nil {
		t.Error("a nil prefetcher claimed to have something ready")
	}
	p.Stop() // must not panic
}

// A depth below two is no pipeline at all: there is nowhere to put the title
// being unpacked ahead.
func TestStartPrefetchRefusesUselessDepths(t *testing.T) {
	s := &Services{}
	for _, depth := range []int{-1, 0, 1} {
		if p := s.StartPrefetch(context.Background(), depth, InstallOptions{}); p != nil {
			t.Errorf("depth %d started a prefetcher", depth)
			p.Stop()
		}
	}
}

// A dry run writes nothing, so unpacking gigabytes for it would be work done
// to be thrown away.
func TestStartPrefetchDoesNothingForADryRun(t *testing.T) {
	s := &Services{DryRun: true}
	if p := s.StartPrefetch(context.Background(), 2, InstallOptions{}); p != nil {
		t.Error("a dry run started unpacking")
		p.Stop()
	}
}

// A title that was never in an archive has nothing to hand over, and must not
// make the installer wait for something that will never arrive.
func TestTakeOnALooseImageDoesNotBlock(t *testing.T) {
	p := newTestPrefetcher(archived("A"), archived("B"), archived("C"), archived("Ico"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := p.Take(context.Background(), model.Game{Title: "Loose", SourcePath: "/roms/loose.iso"}); ok {
			t.Error("a loose image was reported as prepared")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Take blocked on a title that is not archived")
	}
}

// Taking a title still being unpacked waits for it rather than starting a
// second copy of the same extraction.
func TestTakeWaitsForAnUnpackInProgress(t *testing.T) {
	g := archived("Ico")
	p := newTestPrefetcher(archived("A"), archived("B"), archived("C"), archived("Ico"))

	var got *PrefetchedSource
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		src, ok := p.Take(context.Background(), g)
		if !ok {
			t.Error("the waiter was not handed the title")
			return
		}
		got = src
	}()

	// Let the waiter register before the value arrives.
	time.Sleep(50 * time.Millisecond)
	want := &PrefetchedSource{Path: "/scratch/ico.iso", Release: func() {}}
	p.publish(g, want)
	wg.Wait()
	if got != want {
		t.Errorf("got %+v, want the published source", got)
	}
	// And it is handed over once: a second Take falls back to inline.
	if _, ok := p.Take(context.Background(), g); ok {
		t.Error("the same unpacked title was handed out twice")
	}
}

// An unpack that failed is published as nothing, so the installer extracts
// inline and reports the real error in its own turn. A failure to unpack ahead
// is not a failed title.
func TestAFailedUnpackFallsBackToInline(t *testing.T) {
	g := archived("Ico")
	p := newTestPrefetcher(archived("A"), archived("B"), archived("C"), archived("Ico"))
	p.publish(g, nil)
	if src, ok := p.Take(context.Background(), g); ok || src != nil {
		t.Error("a failed unpack was handed over as if it had worked")
	}
}

// Stop releases what was unpacked but never reached -- an interrupted run must
// not leave gigabytes behind.
func TestStopReleasesWhatWasNeverTaken(t *testing.T) {
	p := newTestPrefetcher(archived("A"), archived("B"), archived("C"), archived("Ico"))
	released := 0
	for _, title := range []string{"A", "B"} {
		p.publish(archived(title), &PrefetchedSource{Release: func() { released++ }})
	}
	p.Stop()
	if released != 2 {
		t.Errorf("released %d of 2 unpacked titles", released)
	}
	// After stopping, Take falls back to inline rather than waiting forever.
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := p.Take(context.Background(), archived("C")); ok {
			t.Error("a stopped prefetcher handed something over")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Take blocked after Stop")
	}
}

// A cancelled context must release the installer rather than leave it waiting
// on a prefetcher that has stopped working.
func TestTakeReturnsOnCancellation(t *testing.T) {
	p := newTestPrefetcher(archived("A"), archived("B"), archived("C"), archived("Ico"))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := p.Take(ctx, archived("Ico")); ok {
			t.Error("a cancelled Take reported a title")
		}
	}()
	time.Sleep(50 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Take ignored cancellation")
	}
}

// A title the prefetcher was never given must not make the installer wait for
// it. Before this was guarded, Take blocked until the whole run finished --
// hours, on a library-sized list.
func TestTakeOnATitleNeverQueued(t *testing.T) {
	p := newTestPrefetcher(archived("Ico"))
	done := make(chan struct{})
	go func() {
		defer close(done)
		if _, ok := p.Take(context.Background(), archived("Not Queued")); ok {
			t.Error("a title that was never queued was reported as prepared")
		}
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Take blocked on a title the prefetcher never had")
	}
}

// Two properties, and the first version of this tested only one of them.
//
// The bound is a disk budget: each unpacked title is up to 4.7 GB, so at most
// `depth` copies may exist at once. The overlap is the entire point: while one
// title is being installed, the next must be being unpacked.
//
// Testing only the bound is what let the pipeline ship pipelining nothing. A
// slot is held from before the unpack until the installer has finished with the
// copy, so sizing the channel at depth-1 left room for one copy in total and
// forced strict alternation -- which satisfies "never more than two" perfectly
// well, by never having more than one.
func TestPrefetchOverlapsTheInstall(t *testing.T) {
	games := []model.Game{archived("A"), archived("B"), archived("C")}
	p := newTestPrefetcher(games...)
	begun := make(chan string, len(games))
	p.unpackFn = func(_ context.Context, g model.Game) (*PrefetchedSource, error) {
		begun <- g.Title
		return &PrefetchedSource{Release: func() {}}, nil
	}
	go p.run(context.Background(), make(chan struct{}, slotsFor(2)))

	if got := <-begun; got != "A" {
		t.Fatalf("unpacked %s first, want A", got)
	}
	// The installer takes A and is now busy writing it. It has not released
	// the copy -- installPS2 defers that to the end -- so this is exactly the
	// state the pipeline exists to exploit.
	a, ok := p.Take(context.Background(), games[0])
	if !ok {
		t.Fatal("A was not handed over")
	}
	select {
	case got := <-begun:
		if got != "B" {
			t.Errorf("unpacked %s during A's install, want B", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("nothing was unpacked while A was being installed: the pipeline is not overlapping")
	}
	// And no further: two copies on disk is the budget, and C must wait.
	select {
	case got := <-begun:
		t.Errorf("unpacked %s as well; depth 2 allows two copies, not three", got)
	case <-time.After(200 * time.Millisecond):
	}
	// Finishing with A frees its copy, and C follows.
	a.Release()
	select {
	case got := <-begun:
		if got != "C" {
			t.Errorf("unpacked %s after A was released, want C", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("releasing a copy did not free its slot")
	}
	p.Stop()
}

// A slot stands for a copy on disk, not for a copy waiting to be collected --
// it is held from before the unpack until the installer has finished with the
// file. So depth maps to itself: depth 2 means two copies, one being written
// and one being unpacked. Taking one off, on the reasoning that the title being
// installed is not "waiting", leaves room for a single copy and turns the
// pipeline into strict alternation.
func TestSlotsForDepth(t *testing.T) {
	for depth, want := range map[int]int{2: 2, 3: 3, 4: 4} {
		if got := slotsFor(depth); got != want {
			t.Errorf("depth %d gives %d slots, want %d", depth, got, want)
		}
	}
}
