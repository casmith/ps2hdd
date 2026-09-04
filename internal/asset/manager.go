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

	"golang.org/x/image/draw"

	"github.com/casmith/ps2hdd/internal/asset/provider"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/pfs"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
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

// AppBootName is the launcher filename a PS1 title reaches OPL's Apps page
// under, or "" for anything that is not one.
//
// It is derived from the installed VCD rather than from the title, because the
// launcher is named after the VCD and OPL keys the artwork off the launcher. A
// title that is not installed yet has no VCD name and so no app artwork to
// write; syncing again after installing picks it up.
func AppBootName(g model.Game) string {
	if g.Platform != model.PlatformPS1 {
		return ""
	}
	for _, d := range g.Discs {
		if d.Number <= 1 && d.InstalledName != "" {
			return ps1.LauncherELFName(d.InstalledName)
		}
	}
	if len(g.Discs) > 0 && g.Discs[0].InstalledName != "" {
		return ps1.LauncherELFName(g.Discs[0].InstalledName)
	}
	return ""
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

	// Replaced, never truncated: pfsfuse has no truncate. A failed write is
	// removed rather than left as a partial image OPL would try to draw. See
	// internal/pfs for why.
	out, err := pfs.Create(item.Dest, 0o644)
	if err != nil {
		return 0, err
	}
	n, err := writeForOPL(out, in, item.Game.Platform, item.Type)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(item.Dest)
		return 0, err
	}
	return n, nil
}

// EnsureAppArtwork gives every installed PS1 title a copy of its artwork under
// the name OPL's Apps page looks for, and reports how many it wrote.
//
// A sweep rather than part of the plan, because a plan contains only the slots
// that are missing: a title whose cover is already installed produces no plan
// item at all, and everything installed before OPL's app artwork lookup was
// understood is in exactly that state. Looking at what is on the partition
// catches those; looking at what was just downloaded never would.
//
// Copies rather than links: PFS over FUSE has no hard links, and one cover is a
// hundred kilobytes against a disc of several hundred megabytes.
func (m *Manager) EnsureAppArtwork(games []model.Game, oplMount string) (int, error) {
	written := 0
	for _, g := range games {
		boot := AppBootName(g)
		if boot == "" {
			continue
		}
		for _, t := range model.ArtTypes {
			src := Path(oplMount, g.GameID, t)
			if _, err := os.Stat(src); err != nil {
				continue
			}
			dest := filepath.Join(oplMount, Dir(t), AppFilename(boot, t))
			if _, err := os.Stat(dest); err == nil && !m.Overwrite {
				continue
			}
			if err := copyOPLFile(src, dest); err != nil {
				os.Remove(dest)
				return written, fmt.Errorf("write the Apps-page copy of %s: %w", filepath.Base(src), err)
			}
			written++
		}
	}
	return written, nil
}

// copyOPLFile duplicates a file already written into the +OPL partition.
func copyOPLFile(src, dest string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := pfs.Create(dest, 0o644)
	if err != nil {
		return err
	}
	_, err = io.Copy(out, in)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	return err
}

// writeForOPL writes the asset to out as a PNG at the size OPL expects.
//
// Two things have to be true of the file that lands on the HDD, and neither is
// true of everything a provider serves.
//
// It has to be a PNG. Art files are named <serial>_COV.png and OPL picks its
// decoder from that extension, so a JPEG copied byte-for-byte into that name
// is a file the console cannot draw. The xlenore collections PCSX2 uses are
// all JPEG.
//
// It has to be the documented size, and undersized is not the failure mode --
// oversized is. OPL's texSizeValidate (src/textures.c) refuses any texture
// costing more than maxSize = 720*512*4 = 1,474,560 bytes, returns
// ERR_BAD_DIMENSION, and draws nothing at all with no message. The xlenore
// covers are 512x736, which as truecolor RGB is 1,507,328 bytes: over the
// limit by 2%, and silently invisible on the console. Whatever the source
// gives is scaled to the slot. model.Dimensions is the authority, and a slot
// it does not pin is written through at its natural size. See
// docs/compatibility.md for the arithmetic.
//
// CFG entries are text and are copied untouched.
func writeForOPL(out io.Writer, in io.Reader, platform model.Platform, t model.AssetType) (int64, error) {
	if t == model.AssetConfig {
		return io.Copy(out, in)
	}

	head := make([]byte, len(pngMagic))
	nRead, err := io.ReadFull(in, head)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return 0, err
	}
	body := io.MultiReader(bytes.NewReader(head[:nRead]), in)
	isPNG := nRead == len(pngMagic) && bytes.Equal(head, pngMagic)

	want, pinned := model.Dimensions(platform, t)

	// A PNG already at the right size is copied through untouched, which keeps
	// a database built for OPL byte-identical to what it published.
	if isPNG && !pinned {
		return io.Copy(out, body)
	}
	if isPNG {
		buf, err := io.ReadAll(body)
		if err != nil {
			return 0, err
		}
		if cfg, err := png.DecodeConfig(bytes.NewReader(buf)); err == nil &&
			cfg.Width == want.Width && cfg.Height == want.Height {
			return io.Copy(out, bytes.NewReader(buf))
		}
		body = bytes.NewReader(buf)
	}

	img, format, err := image.Decode(body)
	if err != nil {
		return 0, fmt.Errorf("the artwork is neither PNG nor a format that could be decoded: %w", err)
	}
	if pinned {
		b := img.Bounds()
		if b.Dx() != want.Width || b.Dy() != want.Height {
			scaled := image.NewRGBA(image.Rect(0, 0, want.Width, want.Height))
			// CatmullRom because most of this work is downscaling a cover by a
			// large factor, where a cheaper kernel loses the cover text.
			draw.CatmullRom.Scale(scaled, scaled.Bounds(), img, b, draw.Over, nil)
			img = scaled
		}
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
