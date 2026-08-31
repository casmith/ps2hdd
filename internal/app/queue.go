package app

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/casmith/ps2hdd/internal/model"
)

// QueueState is the lifecycle of one queued install.
type QueueState string

const (
	QueueWaiting    QueueState = "waiting"
	QueueInspecting QueueState = "inspecting"
	// QueueExtracting is unpacking an archived source. It is its own state
	// because it is its own wait -- a minute of decompression reported as
	// "installing" is indistinguishable from a stall.
	QueueExtracting QueueState = "extracting"
	QueueConverting QueueState = "converting"
	QueueInstalling QueueState = "installing"
	QueueVerifying  QueueState = "verifying"
	QueueSyncAssets QueueState = "syncing_assets"
	QueueComplete   QueueState = "complete"
	QueueFailed     QueueState = "failed"
	QueueCancelled  QueueState = "cancelled"
)

// Terminal reports whether a state is final.
func (s QueueState) Terminal() bool {
	return s == QueueComplete || s == QueueFailed || s == QueueCancelled
}

// stateForStage maps an operation stage onto a queue state.
func stateForStage(st Stage) QueueState {
	switch st {
	case StageInspecting:
		return QueueInspecting
	case StageValidating:
		return QueueInspecting
	case StageExtracting:
		return QueueExtracting
	case StageConverting:
		return QueueConverting
	case StageInstalling, StageRemoving:
		return QueueInstalling
	case StageVerifying:
		return QueueVerifying
	case StageSyncAssets:
		return QueueSyncAssets
	case StageComplete:
		return QueueComplete
	default:
		return QueueInstalling
	}
}

// QueueItem is one queued install.
type QueueItem struct {
	ID    int        `json:"id"`
	Game  model.Game `json:"game"`
	State QueueState `json:"state"`
	// Progress is 0..1, or negative when the current stage reports none.
	Progress   float64 `json:"progress"`
	StatusText string  `json:"status_text"`
	Err        string  `json:"error,omitempty"`
	// Warnings carries what the install completed in spite of. A PS1 game
	// that installed cleanly but is not listable by the console is a success
	// with a caveat, and the caveat is the whole point of noticing it.
	Warnings []string  `json:"warnings,omitempty"`
	Started  time.Time `json:"started,omitempty"`
	Finished time.Time `json:"finished,omitempty"`
}

// Queue runs installs one at a time.
//
// Serialisation is not a simplification: hdl_dump writes the raw APA table,
// and running two of them against one disk is not something the tool promises
// to survive. Artwork downloads inside an install are still concurrent, which
// is where the parallelism actually helps.
type Queue struct {
	svc *Services

	mu       sync.Mutex
	items    []*QueueItem
	nextID   int
	running  bool
	cancel   context.CancelFunc
	onUpdate func(QueueItem)
	// prefetch unpacks ahead of the worker, for as long as the worker runs.
	prefetch *Prefetcher
	// opts is the install configuration applied to every queued item.
	opts InstallOptions
}

// NewQueue creates a queue.
func NewQueue(s *Services, opts InstallOptions) *Queue {
	return &Queue{svc: s, nextID: 1, opts: opts}
}

// OnUpdate registers a callback invoked whenever an item changes. It is called
// from the queue's worker goroutine, so the callback must be safe to call from
// there; the TUI forwards to a Bubble Tea message channel.
func (q *Queue) OnUpdate(f func(QueueItem)) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.onUpdate = f
}

// Add appends games to the queue and returns the new items.
func (q *Queue) Add(games ...model.Game) []QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []QueueItem
	for _, g := range games {
		it := &QueueItem{ID: q.nextID, Game: g, State: QueueWaiting, Progress: -1, StatusText: "Waiting"}
		q.nextID++
		q.items = append(q.items, it)
		out = append(out, *it)
	}
	// A pass already under way has to hear about these, or anything queued
	// behind the current work stops being unpacked ahead.
	q.prefetch.Add(games...)
	return out
}

// installOpts is the queue's install configuration with the running pass's
// prefetcher attached. Forgetting to attach it is invisible -- every install
// still works, just without ever overlapping anything -- so it is a step of
// its own rather than two lines inside the worker.
func (q *Queue) installOpts() InstallOptions {
	q.mu.Lock()
	defer q.mu.Unlock()
	o := q.opts
	o.Prefetch = q.prefetch
	return o
}

// waitingGames lists what the worker has still to do, in order.
func (q *Queue) waitingGames() []model.Game {
	q.mu.Lock()
	defer q.mu.Unlock()
	var out []model.Game
	for _, it := range q.items {
		if it.State == QueueWaiting {
			out = append(out, it.Game)
		}
	}
	return out
}

// Items returns a snapshot of the queue.
func (q *Queue) Items() []QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	out := make([]QueueItem, len(q.items))
	for i, it := range q.items {
		out[i] = *it
	}
	return out
}

// Pending reports how many items have not finished.
func (q *Queue) Pending() int {
	q.mu.Lock()
	defer q.mu.Unlock()
	n := 0
	for _, it := range q.items {
		if !it.State.Terminal() {
			n++
		}
	}
	return n
}

// Running reports whether the worker is active.
func (q *Queue) Running() bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.running
}

// Start begins processing. It returns immediately; work happens in a
// goroutine so the TUI stays responsive.
func (q *Queue) Start(ctx context.Context) {
	q.mu.Lock()
	if q.running {
		q.mu.Unlock()
		return
	}
	q.running = true
	ctx, cancel := context.WithCancel(ctx)
	q.cancel = cancel
	q.mu.Unlock()

	go q.run(ctx)
}

// Cancel stops the queue. The item in flight is cancelled through its context;
// items not yet started are marked cancelled.
//
// Cancelling mid-write leaves whatever hdl_dump had written in place. ps2hdd
// does not try to roll that back: unwinding a partial APA write is exactly the
// kind of repair this program refuses to attempt. The remedy is to remove the
// partial game, which `ps2hdd list` will show.
func (q *Queue) Cancel() {
	q.mu.Lock()
	cancel := q.cancel
	for _, it := range q.items {
		if it.State == QueueWaiting {
			it.State = QueueCancelled
			it.StatusText = "Cancelled"
		}
	}
	q.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// Retry moves a failed item back to waiting so Start picks it up again.
func (q *Queue) Retry(id int) bool {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range q.items {
		if it.ID == id && (it.State == QueueFailed || it.State == QueueCancelled) {
			it.State = QueueWaiting
			it.StatusText = "Waiting"
			it.Err = ""
			it.Progress = -1
			return true
		}
	}
	return false
}

// Clear removes finished items.
func (q *Queue) Clear() {
	q.mu.Lock()
	defer q.mu.Unlock()
	kept := q.items[:0]
	for _, it := range q.items {
		if !it.State.Terminal() {
			kept = append(kept, it)
		}
	}
	q.items = kept
}

func (q *Queue) run(ctx context.Context) {
	// Unpacking the next title while the current one is written, the same as
	// the command line does. It lives for one pass of the worker: whatever it
	// has unpacked and not handed over is released when the pass ends, so a
	// cancelled run leaves nothing behind.
	pre := q.svc.StartPrefetch(ctx, q.svc.Config.Install.Prefetch, q.opts)
	q.mu.Lock()
	q.prefetch = pre
	q.mu.Unlock()
	pre.Add(q.waitingGames()...)

	defer func() {
		q.mu.Lock()
		q.running = false
		q.cancel = nil
		q.prefetch = nil
		q.mu.Unlock()
		pre.Stop()
	}()

	for {
		it := q.nextWaiting()
		if it == nil {
			return
		}
		q.process(ctx, it)
		if ctx.Err() != nil {
			q.cancelRemaining()
			return
		}
	}
}

func (q *Queue) nextWaiting() *QueueItem {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, it := range q.items {
		if it.State == QueueWaiting {
			return it
		}
	}
	return nil
}

func (q *Queue) cancelRemaining() {
	q.mu.Lock()
	var pending []QueueItem
	for _, it := range q.items {
		if !it.State.Terminal() {
			it.State = QueueCancelled
			it.StatusText = "Cancelled"
			pending = append(pending, *it)
		}
	}
	cb := q.onUpdate
	q.mu.Unlock()

	if cb != nil {
		for _, snapshot := range pending {
			cb(snapshot)
		}
	}
}

func (q *Queue) process(ctx context.Context, it *QueueItem) {
	q.update(it, func(i *QueueItem) {
		i.State = QueueInspecting
		i.StatusText = "Preparing"
		i.Started = time.Now()
		i.Progress = -1
	})

	opts := q.installOpts()
	opts.OnProgress = func(p Progress) {
		// The queue owns the terminal state, not the operation's progress
		// reporting. Install emits a final StageComplete, and letting that
		// through would mark the item done twice -- which matters because a
		// consumer reasonably treats QueueComplete as "reload the library",
		// and would then do it twice per install.
		state := stateForStage(p.Stage)
		if state.Terminal() {
			return
		}
		q.update(it, func(i *QueueItem) {
			i.State = state
			i.Progress = p.Fraction
			i.StatusText = stageText(p)
		})
	}

	rep, err := q.svc.Install(ctx, it.Game, opts)
	q.update(it, func(i *QueueItem) {
		i.Finished = time.Now()
		i.Warnings = rep.Warnings
		switch {
		case err == nil:
			i.State = QueueComplete
			i.Progress = 1
			i.StatusText = "Complete"
		case errors.Is(err, context.Canceled):
			i.State = QueueCancelled
			i.StatusText = "Cancelled"
			i.Err = ""
		default:
			i.State = QueueFailed
			i.StatusText = "Failed"
			i.Err = err.Error()
		}
	})
}

func stageText(p Progress) string {
	switch p.Stage {
	case StageValidating:
		return "Checking the HDD"
	case StageInspecting:
		return "Inspecting"
	case StageExtracting:
		return "Unpacking"
	case StageConverting:
		return "Converting to VCD"
	case StageInstalling:
		return "Installing"
	case StageRemoving:
		return "Removing"
	case StageVerifying:
		return "Verifying"
	case StageSyncAssets:
		return "Syncing artwork"
	case StageComplete:
		return "Complete"
	default:
		return string(p.Stage)
	}
}

// update mutates an item under the lock, then delivers the change to the
// callback with the lock released.
//
// The callback runs synchronously and in order. An earlier version spawned a
// goroutine per notification to avoid calling out while holding the lock,
// which meant progress updates could arrive out of order -- a later
// percentage overtaking an earlier one, or a "complete" landing before the
// "installing" that preceded it. Taking the snapshot under the lock and
// calling out after releasing it gets both properties: no call-out under lock,
// and ordered delivery.
//
// A callback must therefore not block for long. The interface's is a
// non-blocking channel send, which is the intended shape.
func (q *Queue) update(it *QueueItem, f func(*QueueItem)) {
	q.mu.Lock()
	f(it)
	snapshot := *it
	cb := q.onUpdate
	q.mu.Unlock()

	if cb != nil {
		cb(snapshot)
	}
}

// Summary counts the queue by outcome.
func (q *Queue) Summary() (complete, failed, pending int) {
	for _, it := range q.Items() {
		switch {
		case it.State == QueueComplete:
			complete++
		case it.State == QueueFailed:
			failed++
		case !it.State.Terminal():
			pending++
		}
	}
	return
}
