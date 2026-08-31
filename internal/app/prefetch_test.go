package app

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/casmith/ps2hdd/internal/model"
)

// newTestPrefetcher builds one already primed to expect the given titles,
// which is what StartPrefetch does from its list.
func newTestPrefetcher(games ...model.Game) *Prefetcher {
	p := &Prefetcher{
		expect:  map[string]bool{},
		ready:   map[string]*PrefetchedSource{},
		waiters: map[string]chan struct{}{},
		done:    make(chan struct{}),
	}
	for _, g := range games {
		p.expect[prefetchKey(g)] = true
	}
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
		if p := s.StartPrefetch(context.Background(), nil, depth, InstallOptions{}); p != nil {
			t.Errorf("depth %d started a prefetcher", depth)
			p.Stop()
		}
	}
}

// A dry run writes nothing, so unpacking gigabytes for it would be work done
// to be thrown away.
func TestStartPrefetchDoesNothingForADryRun(t *testing.T) {
	s := &Services{DryRun: true}
	if p := s.StartPrefetch(context.Background(), []model.Game{archived("Ico")}, 2, InstallOptions{}); p != nil {
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

// How far ahead it runs is a disk budget before it is anything else: each
// unpacked title is up to 4.7 GB, so an unbounded prefetcher would put a whole
// library in the scratch directory. Depth 2 means one unpacked title waiting
// while another is installed, and no more.
func TestPrefetchStopsAtItsDepth(t *testing.T) {
	games := []model.Game{archived("A"), archived("B"), archived("C"), archived("D")}
	p := newTestPrefetcher(games...)

	var mu sync.Mutex
	var started []string
	begun := make(chan struct{}, len(games))
	p.unpackFn = func(_ context.Context, g model.Game) (*PrefetchedSource, error) {
		mu.Lock()
		started = append(started, g.Title)
		mu.Unlock()
		begun <- struct{}{}
		return &PrefetchedSource{Path: "/scratch/" + g.Title, Release: func() {}}, nil
	}

	slots := make(chan struct{}, 1) // depth 2: one unpacked ahead
	go p.run(context.Background(), games, slots)

	// Exactly one unpack may happen before anything is taken.
	<-begun
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n := len(started)
	mu.Unlock()
	if n != 1 {
		t.Fatalf("unpacked %d titles (%v) before any was taken; depth 2 allows 1", n, started)
	}

	// Taking the first releases the slot, and exactly one more follows.
	src, ok := p.Take(context.Background(), games[0])
	if !ok {
		t.Fatal("the first title was not handed over")
	}
	src.Release()
	<-begun
	time.Sleep(100 * time.Millisecond)
	mu.Lock()
	n = len(started)
	mu.Unlock()
	if n != 2 {
		t.Errorf("unpacked %d titles after one hand-over, want 2", n)
	}
	p.Stop()
}

// The slot belongs to the unpacked copy on disk, so it is released when the
// installer takes it -- not when the extraction finishes. Freeing it early
// would let the next extraction start while the previous copy is still there,
// which is the budget blown by one image.
func TestPrefetchHoldsTheSlotUntilHandOver(t *testing.T) {
	games := []model.Game{archived("A"), archived("B")}
	p := newTestPrefetcher(games...)
	begun := make(chan string, len(games))
	p.unpackFn = func(_ context.Context, g model.Game) (*PrefetchedSource, error) {
		begun <- g.Title
		return &PrefetchedSource{Release: func() {}}, nil
	}
	slots := make(chan struct{}, 1)
	go p.run(context.Background(), games, slots)

	if got := <-begun; got != "A" {
		t.Fatalf("unpacked %s first", got)
	}
	// B must not start while A is unpacked and untaken.
	select {
	case got := <-begun:
		t.Fatalf("unpacked %s while the previous copy was still on disk", got)
	case <-time.After(200 * time.Millisecond):
	}
	src, _ := p.Take(context.Background(), games[0])
	src.Release()
	select {
	case got := <-begun:
		if got != "B" {
			t.Errorf("unpacked %s, want B", got)
		}
	case <-time.After(2 * time.Second):
		t.Error("releasing the copy did not free the slot")
	}
	p.Stop()
}

// Depth counts the title being installed as well as the ones waiting, because
// its unpacked copy is on disk too. Getting this off by one would either idle
// the pipeline or put an extra image in scratch.
func TestSlotsForDepth(t *testing.T) {
	cases := map[int]int{2: 1, 3: 2, 4: 3}
	for depth, want := range cases {
		if got := slotsFor(depth); got != want {
			t.Errorf("depth %d allows %d waiting, want %d", depth, got, want)
		}
	}
}
