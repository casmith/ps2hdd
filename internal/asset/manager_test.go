package asset_test

import (
	"bytes"
	"context"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/asset/provider"
	"github.com/casmith/ps2hdd/internal/model"
)

// pngBytes is a real 1x1 PNG. Art fixtures have to be decodable images now:
// installing re-encodes anything that is not already PNG, because the
// destination filename claims PNG and OPL picks its decoder from that.
func pngBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 1, G: 2, B: 3, A: 255})
	if err := png.Encode(&buf, img); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// jpegBytes is a real 1x1 JPEG, for checking the conversion path.
func jpegBytes(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, 1, 1))
	img.Set(0, 0, color.RGBA{R: 200, G: 100, B: 50, A: 255})
	if err := jpeg.Encode(&buf, img, nil); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func newLocalManager(t *testing.T, mirror string, want []model.AssetType, overwrite bool) *asset.Manager {
	t.Helper()
	p, err := provider.NewRegistry().New("local", provider.Options{Mirror: mirror})
	if err != nil {
		t.Fatal(err)
	}
	cache, err := asset.NewDownloadCacheAt(filepath.Join(t.TempDir(), "cache"))
	if err != nil {
		t.Fatal(err)
	}
	return &asset.Manager{Provider: p, Cache: cache, Want: want, Overwrite: overwrite}
}

func TestPlanAndApplySync(t *testing.T) {
	mirror := t.TempDir()
	for _, name := range []string{"SLUS_210.50_COV.png", "SLUS_210.50_BG.png"} {
		if err := os.WriteFile(filepath.Join(mirror, name), pngBytes(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opl := t.TempDir()
	games := []model.Game{{Platform: model.PlatformPS2, GameID: "SLUS_210.50", Title: "Burnout 3"}}
	want := []model.AssetType{model.AssetCover, model.AssetBackground, model.AssetIcon}

	inv, err := asset.Scan(opl)
	if err != nil {
		t.Fatal(err)
	}
	m := newLocalManager(t, mirror, want, false)
	plan, err := m.PlanSync(context.Background(), games, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("plan has %d items, want 2: %+v", len(plan.Items), plan.Items)
	}
	// The icon is missing from the HDD and absent from the mirror; that must
	// be reported, not silently dropped.
	if len(plan.Unavailable) != 1 || plan.Unavailable[0].Type != model.AssetIcon {
		t.Errorf("unavailable = %+v", plan.Unavailable)
	}

	res, err := m.Apply(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 2 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(opl, "ART", "SLUS_210.50_COV.png"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pngBytes(t)) {
		t.Errorf("installed cover does not match the mirror copy (%d bytes)", len(got))
	}
	if res.Bytes == 0 {
		t.Error("Bytes not accounted")
	}
}

// Artwork a user curated by hand is not ours to replace: OPL offers no way to
// get it back.
func TestSyncNeverOverwritesByDefault(t *testing.T) {
	mirror := t.TempDir()
	if err := os.WriteFile(filepath.Join(mirror, "SLUS_210.50_COV.png"), pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	opl := t.TempDir()
	if err := os.MkdirAll(filepath.Join(opl, "ART"), 0o755); err != nil {
		t.Fatal(err)
	}
	dest := filepath.Join(opl, "ART", "SLUS_210.50_COV.png")
	if err := os.WriteFile(dest, []byte("hand-picked"), 0o600); err != nil {
		t.Fatal(err)
	}

	games := []model.Game{{Platform: model.PlatformPS2, GameID: "SLUS_210.50"}}
	want := []model.AssetType{model.AssetCover}
	inv, err := asset.Scan(opl)
	if err != nil {
		t.Fatal(err)
	}

	m := newLocalManager(t, mirror, want, false)
	plan, err := m.PlanSync(context.Background(), games, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 0 {
		t.Fatalf("plan would touch an existing file: %+v", plan.Items)
	}
	if got, _ := os.ReadFile(dest); string(got) != "hand-picked" {
		t.Errorf("existing artwork changed to %q", got)
	}

	// With Overwrite the same sync does replace it, which is the documented
	// meaning of the flag.
	m2 := newLocalManager(t, mirror, want, true)
	plan2, err := m2.PlanSync(context.Background(), games, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan2.Items) != 1 || !plan2.Items[0].Exists {
		t.Fatalf("overwrite plan = %+v", plan2.Items)
	}
	if _, err := m2.Apply(context.Background(), plan2, nil); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dest); !bytes.Equal(got, pngBytes(t)) {
		t.Errorf("overwrite did not replace the hand-picked file (%d bytes)", len(got))
	}
}

func TestApplyReportsProgress(t *testing.T) {
	mirror := t.TempDir()
	for _, n := range []string{"SLUS_210.50_COV.png", "SLUS_215.03_COV.png"} {
		if err := os.WriteFile(filepath.Join(mirror, n), pngBytes(t), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	opl := t.TempDir()
	inv, _ := asset.Scan(opl)
	m := newLocalManager(t, mirror, []model.AssetType{model.AssetCover}, false)
	plan, err := m.PlanSync(context.Background(), []model.Game{
		{Platform: model.PlatformPS2, GameID: "SLUS_210.50"},
		{Platform: model.PlatformPS2, GameID: "SLUS_215.03"},
	}, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	var seen []int
	if _, err := m.Apply(context.Background(), plan, func(done, total int, _ asset.PlanItem) {
		if total != 2 {
			t.Errorf("total = %d", total)
		}
		seen = append(seen, done)
	}); err != nil {
		t.Fatal(err)
	}
	if len(seen) != 2 || seen[0] != 1 || seen[1] != 2 {
		t.Errorf("progress = %v, want 1 then 2", seen)
	}
}

func TestDownloadCache(t *testing.T) {
	c, err := asset.NewDownloadCacheAt(filepath.Join(t.TempDir(), "art"))
	if err != nil {
		t.Fatal(err)
	}
	a := model.Asset{Type: model.AssetCover, GameID: "SLUS_210.50", Source: "https://x/y.png"}
	if _, ok := c.Get(a); ok {
		t.Fatal("empty cache reported a hit")
	}
	p, err := c.Put(a, stringReader("cover"))
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := c.Get(a); !ok || got != p {
		t.Errorf("Get = %q,%v", got, ok)
	}
	// A different source URL is a different cache entry even for the same slot.
	b := a
	b.Source = "https://other/y.png"
	if _, ok := c.Get(b); ok {
		t.Error("cache collided across sources")
	}
	if n, err := c.Size(); err != nil || n != 5 {
		t.Errorf("Size = %d, %v", n, err)
	}
	if n, err := c.Clean(); err != nil || n != 1 {
		t.Errorf("Clean = %d, %v", n, err)
	}
	if _, ok := c.Get(a); ok {
		t.Error("Clean left an entry behind")
	}
}

func TestDownloadCacheRejectsEmpty(t *testing.T) {
	c, err := asset.NewDownloadCacheAt(filepath.Join(t.TempDir(), "art"))
	if err != nil {
		t.Fatal(err)
	}
	// An empty body from a misbehaving mirror must not become a zero-byte
	// cover that OPL then fails to draw.
	if _, err := c.Put(model.Asset{GameID: "SLUS_210.50", Type: model.AssetCover}, stringReader("")); err == nil {
		t.Fatal("cache accepted an empty download")
	}
}

func TestStatusRows(t *testing.T) {
	opl := t.TempDir()
	if err := os.MkdirAll(filepath.Join(opl, "ART"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(opl, "ART", "SLUS_210.50_COV.png"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	inv, err := asset.Scan(opl)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.AssetType{model.AssetCover, model.AssetBackground}
	rows := asset.Status([]model.Game{{GameID: "SLUS_210.50", Title: "Burnout 3"}}, inv, want)
	if len(rows) != 1 {
		t.Fatal("no rows")
	}
	if !rows[0].Present[model.AssetCover] || rows[0].Present[model.AssetBackground] {
		t.Errorf("present = %+v", rows[0].Present)
	}
	if len(rows[0].Missing) != 1 || rows[0].Missing[0] != model.AssetBackground {
		t.Errorf("missing = %v", rows[0].Missing)
	}
}

type sr struct{ s string }

func (r *sr) Read(p []byte) (int, error) {
	if r.s == "" {
		return 0, io.EOF
	}
	n := copy(p, r.s)
	r.s = r.s[n:]
	return n, nil
}

func stringReader(s string) *sr { return &sr{s: s} }

// A database that serves JPEG must not have its bytes copied into a file named
// .png: OPL picks its decoder from the extension, so the console would be
// handed a file it cannot draw. The bytes are re-encoded instead.
func TestInstallConvertsJPEGToPNG(t *testing.T) {
	mirror := t.TempDir()
	if err := os.WriteFile(filepath.Join(mirror, "SLUS_210.50_COV.jpg"), jpegBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	opl := t.TempDir()
	games := []model.Game{{Platform: model.PlatformPS2, GameID: "SLUS_210.50", Title: "Burnout 3"}}
	want := []model.AssetType{model.AssetCover}

	inv, err := asset.Scan(opl)
	if err != nil {
		t.Fatal(err)
	}
	m := newLocalManager(t, mirror, want, false)
	plan, err := m.PlanSync(context.Background(), games, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Apply(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}
	got, err := os.ReadFile(filepath.Join(opl, "ART", "SLUS_210.50_COV.png"))
	if err != nil {
		t.Fatal(err)
	}
	if _, format, err := image.Decode(bytes.NewReader(got)); err != nil || format != "png" {
		t.Errorf("installed file is %q (err %v), want png", format, err)
	}
}

// Anything that is not a decodable image is refused rather than written: a
// file OPL cannot draw is worse than an absent one, because it looks installed.
func TestInstallRefusesUndecodableArt(t *testing.T) {
	mirror := t.TempDir()
	if err := os.WriteFile(filepath.Join(mirror, "SLUS_210.50_COV.png"), []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	opl := t.TempDir()
	games := []model.Game{{Platform: model.PlatformPS2, GameID: "SLUS_210.50", Title: "Burnout 3"}}

	inv, err := asset.Scan(opl)
	if err != nil {
		t.Fatal(err)
	}
	m := newLocalManager(t, mirror, []model.AssetType{model.AssetCover}, false)
	plan, err := m.PlanSync(context.Background(), games, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Apply(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 0 || len(res.Failed) != 1 {
		t.Fatalf("result = %+v", res)
	}
	if _, err := os.Stat(filepath.Join(opl, "ART", "SLUS_210.50_COV.png")); !os.IsNotExist(err) {
		t.Error("an undecodable file was left on the HDD")
	}
}

// Overwriting must unlink the old file rather than truncate it.
//
// pfsfuse implements ftruncate but not truncate, so O_TRUNC on an existing
// file comes back ENOSYS and every overwrite fails while every first write
// succeeds. The mechanism is asserted through the inode: truncating keeps it,
// unlinking and recreating does not.
func TestOverwriteReplacesTheFileRatherThanTruncatingIt(t *testing.T) {
	mirror := t.TempDir()
	if err := os.WriteFile(filepath.Join(mirror, "SLUS_210.50_COV.png"), pngBytes(t), 0o600); err != nil {
		t.Fatal(err)
	}
	opl := t.TempDir()
	dest := filepath.Join(opl, "ART", "SLUS_210.50_COV.png")
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(dest, []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}

	games := []model.Game{{Platform: model.PlatformPS2, GameID: "SLUS_210.50", Title: "Burnout 3"}}
	inv, err := asset.Scan(opl)
	if err != nil {
		t.Fatal(err)
	}
	m := newLocalManager(t, mirror, []model.AssetType{model.AssetCover}, true)
	plan, err := m.PlanSync(context.Background(), games, inv, opl)
	if err != nil {
		t.Fatal(err)
	}
	res, err := m.Apply(context.Background(), plan, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Installed) != 1 || len(res.Failed) != 0 {
		t.Fatalf("result = %+v", res)
	}

	after, err := os.Stat(dest)
	if err != nil {
		t.Fatal(err)
	}
	if sameFile(before, after) {
		t.Error("the file was truncated in place; pfsfuse has no truncate and would refuse it")
	}
	got, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, pngBytes(t)) {
		t.Errorf("overwrite did not install the new bytes (%d bytes)", len(got))
	}
}

func sameFile(a, b os.FileInfo) bool { return os.SameFile(a, b) }
