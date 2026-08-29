package asset

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/casmith/ps2hdd/internal/asset/provider"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
)

// Manager plans and performs artwork synchronisation.
type Manager struct {
	Provider provider.Provider
	Cache    *DownloadCache
	// Want is the set of asset types the user enabled.
	Want []model.AssetType
	// Overwrite replaces files that already exist on the HDD. It defaults to
	// false: artwork a user curated by hand is not ours to replace, and OPL
	// gives no way to get it back.
	Overwrite bool
	// Concurrency bounds parallel downloads.
	Concurrency int
}

// Plan is what a sync would do.
type Plan struct {
	Items []PlanItem `json:"items"`
	// Unavailable lists slots that are missing on the HDD and that no
	// configured provider can supply, so the user learns why the gap stays.
	Unavailable []PlanItem `json:"unavailable,omitempty"`
}

// PlanItem is one asset to install.
type PlanItem struct {
	Game  model.Game      `json:"game"`
	Type  model.AssetType `json:"type"`
	Asset model.Asset     `json:"asset,omitempty"`
	// Dest is the path inside the mounted +OPL partition.
	Dest string `json:"dest"`
	// Exists records that the file is already present, which happens when
	// Overwrite is on.
	Exists bool `json:"exists,omitempty"`
}

// Result is the outcome of applying a plan.
type Result struct {
	Installed []PlanItem   `json:"installed"`
	Skipped   []PlanItem   `json:"skipped,omitempty"`
	Failed    []FailedItem `json:"failed,omitempty"`
	Bytes     int64        `json:"bytes"`
}

// FailedItem records why one asset could not be installed.
type FailedItem struct {
	PlanItem
	Reason string `json:"reason"`
}

// PlanSync works out which assets are missing and which of those the provider
// can supply. It performs lookups but no downloads and no writes.
func (m *Manager) PlanSync(ctx context.Context, games []model.Game, inv *Inventory, oplMount string) (Plan, error) {
	var plan Plan
	for _, g := range games {
		want := m.wantFor(g, inv)
		if len(want) == 0 {
			continue
		}
		set, err := m.Provider.Lookup(ctx, g, want)
		if err != nil {
			// A lookup failure for one game must not abort the whole plan;
			// record every slot as unavailable and carry on.
			logging.ContextLogger(ctx).Warn("artwork lookup failed", "game", g.GameID, "err", err)
		}
		have := map[model.AssetType]model.Asset{}
		for _, a := range set.Assets {
			have[a.Type] = a
		}
		for _, t := range want {
			item := PlanItem{Game: g, Type: t, Dest: Path(oplMount, g.GameID, t)}
			a, ok := have[t]
			if !ok {
				plan.Unavailable = append(plan.Unavailable, item)
				continue
			}
			a.Filename = Filename(g.GameID, t)
			item.Asset = a
			if _, err := os.Stat(item.Dest); err == nil {
				item.Exists = true
			}
			plan.Items = append(plan.Items, item)
		}
	}
	return plan, nil
}

// wantFor returns the asset types to fetch for a game: the configured set,
// minus what is already installed unless Overwrite is on.
func (m *Manager) wantFor(g model.Game, inv *Inventory) []model.AssetType {
	if m.Overwrite {
		return append([]model.AssetType(nil), m.Want...)
	}
	if inv == nil {
		return append([]model.AssetType(nil), m.Want...)
	}
	return inv.Missing(g.GameID, m.Want)
}

// Apply downloads and installs the assets in a plan.
//
// Downloads run concurrently and are cached; the writes into the mounted +OPL
// partition are serialised, because a FUSE mount over a raw ATA disk is not a
// place to have several writers at once.
func (m *Manager) Apply(ctx context.Context, plan Plan, onProgress func(done, total int, item PlanItem)) (Result, error) {
	var res Result
	if len(plan.Items) == 0 {
		return res, nil
	}
	conc := m.Concurrency
	if conc <= 0 {
		conc = 4
	}

	type fetched struct {
		item  PlanItem
		local string
		err   error
	}
	results := make([]fetched, len(plan.Items))
	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	for i, item := range plan.Items {
		wg.Add(1)
		go func(i int, item PlanItem) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			if err := ctx.Err(); err != nil {
				results[i] = fetched{item: item, err: err}
				return
			}
			path, err := m.fetch(ctx, item.Asset)
			results[i] = fetched{item: item, local: path, err: err}
		}(i, item)
	}
	wg.Wait()

	var writeMu sync.Mutex
	writeMu.Lock()
	defer writeMu.Unlock()
	for i, f := range results {
		if onProgress != nil {
			onProgress(i+1, len(plan.Items), f.item)
		}
		if f.err != nil {
			if errors.Is(f.err, provider.ErrNotAvailable) {
				res.Skipped = append(res.Skipped, f.item)
				continue
			}
			res.Failed = append(res.Failed, FailedItem{PlanItem: f.item, Reason: f.err.Error()})
			continue
		}
		n, err := m.install(f.local, f.item)
		if err != nil {
			res.Failed = append(res.Failed, FailedItem{PlanItem: f.item, Reason: err.Error()})
			continue
		}
		res.Installed = append(res.Installed, f.item)
		res.Bytes += n
	}
	return res, nil
}

// fetch returns a local path holding the asset's bytes, downloading it only if
// it is not already cached.
func (m *Manager) fetch(ctx context.Context, a model.Asset) (string, error) {
	if m.Cache != nil {
		if p, ok := m.Cache.Get(a); ok {
			return p, nil
		}
	}
	rc, err := m.Provider.Fetch(ctx, a)
	if err != nil {
		return "", err
	}
	defer rc.Close()
	if m.Cache == nil {
		return "", fmt.Errorf("no artwork cache configured")
	}
	return m.Cache.Put(a, rc)
}

// install copies a cached asset into the mounted +OPL partition.
func (m *Manager) install(src string, item PlanItem) (int64, error) {
	if item.Exists && !m.Overwrite {
		return 0, nil
	}
	if err := os.MkdirAll(filepath.Dir(item.Dest), 0o755); err != nil {
		return 0, err
	}
	in, err := os.Open(src)
	if err != nil {
		return 0, err
	}
	defer in.Close()

	// PFS via FUSE does not always support rename, so the destination is
	// written directly. A failed write is removed rather than left as a
	// truncated image OPL would try to draw.
	out, err := os.Create(item.Dest)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(item.Dest)
		return 0, err
	}
	return n, nil
}

// StatusRow is one line of `ps2hdd art status`.
type StatusRow struct {
	Game    model.Game               `json:"game"`
	Present map[model.AssetType]bool `json:"present"`
	Missing []model.AssetType        `json:"missing,omitempty"`
}

// Status builds the artwork report for a set of games.
func Status(games []model.Game, inv *Inventory, want []model.AssetType) []StatusRow {
	rows := make([]StatusRow, 0, len(games))
	for _, g := range games {
		st := inv.Status(g.GameID)
		present := map[model.AssetType]bool{}
		for _, t := range want {
			present[t] = st.Has(t)
		}
		rows = append(rows, StatusRow{Game: g, Present: present, Missing: st.Missing(want)})
	}
	return rows
}
