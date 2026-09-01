package app

import (
	"context"
	"sync"

	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
)

// Unpacking the next title while the current one is being written.
//
// The two halves of an archived install use different machines. Extraction is
// LZMA on one core plus a write to scratch; injection is hdl_dump pushing raw
// sectors at the PS2 drive. Run in sequence they add up, and on a measured
// library the extraction is the larger half -- around seventy seconds for a
// DVD-sized title, during which the drive is idle and fifteen of sixteen cores
// are too, because a solid LZMA stream cannot be split.
//
// So one title is unpacked ahead. Only extraction overlaps: injection stays
// strictly serial, because hdl_dump rewrites the APA partition table and two of
// them at once is not something it survives. The drive lock enforces that
// independently, and nothing here touches it.
//
// Depth is a disk budget before it is a concurrency setting. Each unpacked
// title is up to 4.7 GB, so the useful question is not how many cores are free
// but how many images fit in the scratch directory at once. Depth 2 -- the one
// being installed and the one after it -- is enough to keep the writer fed and
// costs one extra image's worth of space.

// PrefetchedSource is a title unpacked before its turn.
type PrefetchedSource struct {
	// Path is the extracted image, for a PS2 title.
	Path string
	// Discs are the extracted discs, for a PS1 rip.
	Discs []model.Disc
	// Release removes the unpacked copy. It is the caller's to defer, exactly
	// as the inline extraction's cleanup is.
	Release func()
}

// Prefetcher unpacks archived titles ahead of the installer.
//
// It is deliberately one goroutine rather than a pool. Extraction is bound by
// single-stream decompression, so unpacking two titles at once would halve
// neither and would double the scratch space; what is being hidden is the
// write, not another extraction.
type Prefetcher struct {
	svc  *Services
	opts InstallOptions

	mu sync.Mutex
	// expect is what this prefetcher intends to produce, and is the guard
	// against waiting for something that will never arrive. Without it, Take
	// on a title the prefetcher was never given -- or a second Take on one
	// already handed over -- blocks until the entire run finishes, which on a
	// library-sized list is hours.
	expect map[string]bool
	ready  map[string]*PrefetchedSource
	// waiters lets Take block on a title still being unpacked rather than
	// starting a second copy of the same work.
	waiters map[string]chan struct{}
	done    chan struct{}
	stopped bool
	// unpackFn is the extraction, replaceable in tests. The bound on how far
	// ahead this runs is a disk budget measured in gigabytes, and testing it
	// against real archives would mean writing them.
	unpackFn func(context.Context, model.Game) (*PrefetchedSource, error)
	// pending is the work not yet started, and wake signals that more has
	// arrived. The list is fed rather than fixed because the TUI's queue takes
	// additions while it is running: a prefetcher built from a snapshot would
	// silently stop pipelining the moment anything was queued behind it.
	pending []model.Game
	wake    chan struct{}
	// hits counts titles the installer took already unpacked. It is the only
	// evidence the pipeline is doing anything: a run where it is zero spent
	// the whole time extracting and writing in sequence.
	hits int
}

// Hits reports how many titles were handed over already unpacked.
func (p *Prefetcher) Hits() int {
	if p == nil {
		return 0
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.hits
}

// StartPrefetch unpacks the given titles, in order, keeping at most depth-1
// unpacked ahead of whatever the caller has taken.
//
// A depth below 2 disables it and returns nil, which every call site treats as
// "extract inline", so the pipelined and plain paths are the same code.
func (s *Services) StartPrefetch(ctx context.Context, depth int, opts InstallOptions) *Prefetcher {
	if depth < 2 || s.DryRun {
		return nil
	}
	p := &Prefetcher{
		svc:     s,
		opts:    opts,
		expect:  map[string]bool{},
		ready:   map[string]*PrefetchedSource{},
		waiters: map[string]chan struct{}{},
		done:    make(chan struct{}),
		wake:    make(chan struct{}, 1),
	}
	p.unpackFn = p.unpack
	// The buffer is the whole budget: with depth 2 the goroutine unpacks one
	// title, hands it over, and blocks until the installer has taken it. That
	// bounds the scratch directory at two images without counting bytes.
	go p.run(ctx, make(chan struct{}, slotsFor(depth)))
	return p
}

// Add queues titles to unpack, in the order given. Anything not in an archive
// is ignored: there is nothing to unpack and Take says so immediately.
func (p *Prefetcher) Add(games ...model.Game) {
	if p == nil {
		return
	}
	p.mu.Lock()
	if p.stopped {
		p.mu.Unlock()
		return
	}
	for _, g := range games {
		if g.ArchiveMember == "" {
			continue
		}
		k := prefetchKey(g)
		if p.expect[k] {
			continue // already queued
		}
		p.expect[k] = true
		p.pending = append(p.pending, g)
	}
	p.mu.Unlock()
	select {
	case p.wake <- struct{}{}:
	default:
	}
}

// nextPending takes the next title to unpack, waiting for one to arrive.
// ok is false once the prefetcher is stopped or the context is done.
func (p *Prefetcher) nextPending(ctx context.Context) (model.Game, bool) {
	for {
		p.mu.Lock()
		if p.stopped {
			p.mu.Unlock()
			return model.Game{}, false
		}
		if len(p.pending) > 0 {
			g := p.pending[0]
			p.pending = p.pending[1:]
			p.mu.Unlock()
			return g, true
		}
		p.mu.Unlock()
		select {
		case <-p.wake:
		case <-ctx.Done():
			return model.Game{}, false
		}
	}
}

// slotsFor is how many unpacked copies may exist at once, which is the depth
// exactly.
//
// A slot is held from before a title is unpacked until the installer has
// finished with it, so it stands for a copy on disk rather than for a copy
// waiting to be collected. Making it depth-1 -- reasoning that the title being
// installed is not "waiting" -- left room for one copy in total, which forces
// strict alternation: unpack, install, unpack, install, with nothing ever
// happening at the same time as anything else. The pipeline pipelined nothing.
//
// Two slots is what lets the next title be unpacked while the current one is
// written, and the disk budget is unchanged: two slots, two copies, which is
// what depth 2 was always supposed to mean.
func slotsFor(depth int) int { return depth }

func (p *Prefetcher) run(ctx context.Context, slots chan struct{}) {
	defer close(p.done)
	log := logging.ContextLogger(ctx)
	for {
		g, ok := p.nextPending(ctx)
		if !ok {
			return
		}
		select {
		case slots <- struct{}{}:
		case <-ctx.Done():
			return
		}
		if p.isStopped() {
			<-slots
			return
		}
		src, err := p.unpackFn(ctx, g)
		if err != nil {
			// A title that could not be unpacked ahead is not a failed title.
			// The installer will try it again in its turn and report properly
			// then; out of space here just means the pipeline is full.
			log.Debug("could not unpack ahead", "title", g.Title, "err", err)
			<-slots
			p.publish(g, nil)
			continue
		}
		// The slot is released when the installer takes the title, not now.
		taken := src.Release
		src.Release = func() {
			taken()
			<-slots
		}
		p.publish(g, src)
	}
}

// unpack does the platform's extraction, whichever it is.
func (p *Prefetcher) unpack(ctx context.Context, g model.Game) (*PrefetchedSource, error) {
	if g.Platform == model.PlatformPS1 {
		discs, cleanup, err := p.svc.extractPS1Source(ctx, g, p.opts)
		if err != nil {
			return nil, err
		}
		return &PrefetchedSource{Discs: discs, Release: cleanup}, nil
	}
	path, cleanup, err := p.svc.extractSource(ctx, g, p.opts)
	if err != nil {
		return nil, err
	}
	return &PrefetchedSource{Path: path, Release: cleanup}, nil
}

func (p *Prefetcher) publish(g model.Game, src *PrefetchedSource) {
	k := prefetchKey(g)
	p.mu.Lock()
	p.ready[k] = src
	if w, ok := p.waiters[k]; ok {
		close(w)
		delete(p.waiters, k)
	}
	p.mu.Unlock()
}

// Take hands over a title unpacked earlier, waiting if it is still in
// progress. ok is false when there is nothing prepared, which is the signal to
// extract inline: a title that was never archived, one the prefetcher has not
// reached, or one whose unpacking failed and will be retried properly.
func (p *Prefetcher) Take(ctx context.Context, g model.Game) (*PrefetchedSource, bool) {
	if p == nil || g.ArchiveMember == "" {
		return nil, false
	}
	k := prefetchKey(g)
	p.mu.Lock()
	if !p.expect[k] {
		// Never queued, or already handed over. Either way there is nothing
		// coming and the caller should extract inline.
		p.mu.Unlock()
		return nil, false
	}
	if src, ok := p.ready[k]; ok {
		delete(p.ready, k)
		delete(p.expect, k)
		if src != nil {
			p.hits++
		}
		p.mu.Unlock()
		return src, src != nil
	}
	if p.stopped {
		p.mu.Unlock()
		return nil, false
	}
	w, ok := p.waiters[k]
	if !ok {
		w = make(chan struct{})
		p.waiters[k] = w
	}
	p.mu.Unlock()

	select {
	case <-w:
	case <-p.done:
	case <-ctx.Done():
		return nil, false
	}
	p.mu.Lock()
	src := p.ready[k]
	delete(p.ready, k)
	delete(p.expect, k)
	if src != nil {
		p.hits++
	}
	p.mu.Unlock()
	return src, src != nil
}

// Stop ends the prefetching and removes anything unpacked but never taken.
func (p *Prefetcher) Stop() {
	if p == nil {
		return
	}
	p.mu.Lock()
	p.stopped = true
	p.expect = map[string]bool{}
	p.pending = nil
	held := p.ready
	p.ready = map[string]*PrefetchedSource{}
	for k, w := range p.waiters {
		close(w)
		delete(p.waiters, k)
	}
	p.mu.Unlock()
	// Wake the loop so it sees the stop rather than waiting for work that is
	// no longer coming.
	select {
	case p.wake <- struct{}{}:
	default:
	}
	for _, src := range held {
		if src != nil && src.Release != nil {
			src.Release()
		}
	}
}

func (p *Prefetcher) isStopped() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.stopped
}

// prefetchKey identifies a title within one run. The archive and the member
// together are what the extraction is of, and two entries never share both.
func prefetchKey(g model.Game) string { return g.SourcePath + "!" + g.ArchiveMember }
