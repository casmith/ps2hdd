package asset

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	_ "image/gif"  // registered so a GIF from an odd mirror still converts
	_ "image/jpeg" // the xlenore cover databases serve JPEG
	"image/png"
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

	// The destination is removed before it is written, never truncated.
	//
	// os.Create is O_RDWR|O_CREAT|O_TRUNC, and O_TRUNC on a file that already
	// exists makes the kernel ask the filesystem to truncate it. pfsfuse
	// implements ftruncate but not truncate, so that request comes back
	// ENOSYS -- "function not implemented" -- and the result is that every
	// first write to a slot succeeds and every overwrite fails. Unlinking
	// asks for nothing pfsfuse does not implement.
	//
	// PFS via FUSE does not always support rename, so there is no write-aside
	// and swap: the old file goes before the new one arrives. A failed write
	// is removed rather than left as a truncated image OPL would try to draw,
	// which means an interrupted overwrite leaves the slot empty rather than
	// stale. Empty is the better of the two -- the next sync fills it.
	if err := os.Remove(item.Dest); err != nil && !errors.Is(err, os.ErrNotExist) {
		return 0, fmt.Errorf("replace %s: %w", filepath.Base(item.Dest), err)
	}
	out, err := os.OpenFile(item.Dest, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := copyAsPNG(out, in, item.Type)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(item.Dest)
		return 0, err
	}
	return n, nil
}

// copyAsPNG writes the asset to out, re-encoding it if it is not already PNG.
//
// Art files are named <serial>_COV.png and OPL picks its decoder from that
// extension, so a JPEG copied byte-for-byte into that name is a file the
// console cannot draw. Some databases serve JPEG -- the xlenore collections
// PCSX2 uses are all JPEG -- so the bytes have to be converted rather than
// trusted to match the name they are being given.
//
// CFG entries are text and are copied untouched.
func copyAsPNG(out io.Writer, in io.Reader, t model.AssetType) (int64, error) {
	if t == model.AssetConfig {
		return io.Copy(out, in)
	}

	head := make([]byte, len(pngMagic))
	nRead, err := io.ReadFull(in, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	body := io.MultiReader(bytes.NewReader(head[:nRead]), in)

	if nRead == len(pngMagic) && bytes.Equal(head, pngMagic) {
		return io.Copy(out, body)
	}

	img, format, err := image.Decode(body)
	if err != nil {
		return 0, fmt.Errorf("the artwork is neither PNG nor a format that could be decoded: %w", err)
	}
	counter := &countingWriter{w: out}
	if err := png.Encode(counter, img); err != nil {
		return 0, fmt.Errorf("re-encode %s artwork as PNG: %w", format, err)
	}
	return counter.n, nil
}

// pngMagic is the PNG signature.
var pngMagic = []byte{0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a}

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
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
