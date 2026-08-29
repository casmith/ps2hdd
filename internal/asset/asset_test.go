package asset_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/asset/provider"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
)

func TestMain(m *testing.M) {
	logging.Discard()
	os.Exit(m.Run())
}

func TestFilename(t *testing.T) {
	cases := []struct {
		id   string
		typ  model.AssetType
		want string
	}{
		{"SLUS_209.46", model.AssetCover, "SLUS_209.46_COV.png"},
		{"SLUS_209.46", model.AssetBackground, "SLUS_209.46_BG.png"},
		{"SLUS_209.46", model.AssetScreen2, "SLUS_209.46_SCR2.png"},
		{"SLUS_209.46", model.AssetIcon, "SLUS_209.46_ICO.png"},
		{"SLUS_209.46", model.AssetConfig, "SLUS_209.46.cfg"},
		// Whatever form the caller has, OPL's form is what lands on the HDD.
		{"SLUS-20946", model.AssetCover, "SLUS_209.46_COV.png"},
		{"slus20946", model.AssetCover, "SLUS_209.46_COV.png"},
	}
	for _, c := range cases {
		if got := asset.Filename(c.id, c.typ); got != c.want {
			t.Errorf("Filename(%q,%q) = %q, want %q", c.id, c.typ, got, c.want)
		}
	}
	if d := asset.Dir(model.AssetConfig); d != "CFG" {
		t.Errorf("CFG dir = %q", d)
	}
	if d := asset.Dir(model.AssetCover); d != "ART" {
		t.Errorf("ART dir = %q", d)
	}
	if p := asset.Path("/mnt/opl", "SLUS_209.46", model.AssetCover); p != "/mnt/opl/ART/SLUS_209.46_COV.png" {
		t.Errorf("Path = %q", p)
	}
}

func TestParseArtFilename(t *testing.T) {
	cases := []struct {
		name string
		id   string
		typ  model.AssetType
		ok   bool
	}{
		{"SLUS_209.46_COV.png", "SLUS_209.46", model.AssetCover, true},
		{"SLUS_209.46_SCR2.PNG", "SLUS_209.46", model.AssetScreen2, true},
		{"SLUS_209.46_COV.jpg", "SLUS_209.46", model.AssetCover, true},
		{"SLUS_209.46_BOGUS.png", "", "", false},
		{"random.png", "", "", false},
		{"SLUS_209.46_COV.txt", "", "", false},
		{"_COV.png", "", "", false},
	}
	for _, c := range cases {
		id, typ, ok := asset.ParseArtFilename(c.name)
		if ok != c.ok || id != c.id || typ != c.typ {
			t.Errorf("ParseArtFilename(%q) = %q,%q,%v want %q,%q,%v", c.name, id, typ, ok, c.id, c.typ, c.ok)
		}
	}
}

// buildOPL lays out a +OPL partition the way one looks on a real HDD.
func buildOPL(t *testing.T, art, cfg []string) string {
	t.Helper()
	root := t.TempDir()
	for dir, files := range map[string][]string{"ART": art, "CFG": cfg} {
		if len(files) == 0 {
			continue
		}
		d := filepath.Join(root, dir)
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, f := range files {
			if err := os.WriteFile(filepath.Join(d, f), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func TestScanInventory(t *testing.T) {
	root := buildOPL(t,
		[]string{"SLUS_210.50_COV.png", "SLUS_210.50_BG.png", "SLUS_215.03_COV.png", "notes.txt"},
		[]string{"SLUS_210.50.cfg"},
	)
	inv, err := asset.Scan(root)
	if err != nil {
		t.Fatal(err)
	}
	if inv.Games() != 2 {
		t.Errorf("Games = %d, want 2", inv.Games())
	}
	st := inv.Status("SLUS_210.50")
	if !st.Has(model.AssetCover) || !st.Has(model.AssetBackground) || !st.Has(model.AssetConfig) {
		t.Errorf("SLUS_210.50 status = %+v", st.Present)
	}
	want := []model.AssetType{model.AssetCover, model.AssetBackground, model.AssetIcon, model.AssetConfig}
	missing := inv.Missing("SLUS_210.50", want)
	if len(missing) != 1 || missing[0] != model.AssetIcon {
		t.Errorf("missing = %v, want [ICO]", missing)
	}
	// A game with nothing installed is missing everything.
	if got := inv.Missing("SCUS_973.99", want); len(got) != len(want) {
		t.Errorf("unknown game missing = %v", got)
	}
	// A dashed serial must find the same files.
	if !inv.Status("SLUS-21050").Has(model.AssetCover) {
		t.Error("inventory lookup is not id-format agnostic")
	}
}

func TestScanEmptyPartition(t *testing.T) {
	// A fresh +OPL has neither ART nor CFG; that is "nothing present", not an
	// error.
	inv, err := asset.Scan(t.TempDir())
	if err != nil {
		t.Fatalf("Scan of an empty partition: %v", err)
	}
	if inv.Games() != 0 {
		t.Errorf("Games = %d", inv.Games())
	}
}

// stubDoer serves canned HTTP responses.
type stubDoer struct {
	bodies map[string]string
	err    error
	calls  []string
}

func (s *stubDoer) Do(req *http.Request) (*http.Response, error) {
	s.calls = append(s.calls, req.URL.String())
	if s.err != nil {
		return nil, s.err
	}
	body, ok := s.bodies[req.URL.String()]
	if !ok {
		return &http.Response{StatusCode: 404, Status: "404 Not Found", Body: io.NopCloser(strings.NewReader(""))}, nil
	}
	return &http.Response{StatusCode: 200, Status: "200 OK", Body: io.NopCloser(strings.NewReader(body))}, nil
}

func TestPS2CoversProvider(t *testing.T) {
	const url = "https://raw.githubusercontent.com/xlenore/ps2-covers/main/covers/default/SLUS-21050.jpg"
	doer := &stubDoer{bodies: map[string]string{url: "PNGDATA"}}
	reg := provider.NewRegistry()
	p, err := reg.New("ps2-covers", provider.Options{HTTP: doer})
	if err != nil {
		t.Fatal(err)
	}

	game := model.Game{Platform: model.PlatformPS2, GameID: "SLUS_210.50", Title: "Burnout 3"}
	want := []model.AssetType{model.AssetCover, model.AssetBackground, model.AssetIcon}
	set, err := p.Lookup(context.Background(), game, want)
	if err != nil {
		t.Fatal(err)
	}
	// The database holds front covers only. Reporting a background it does not
	// have would put a wrong image on the HDD.
	if len(set.Assets) != 1 || set.Assets[0].Type != model.AssetCover {
		t.Fatalf("Lookup = %+v, want just a cover", set.Assets)
	}
	if set.Assets[0].Source != url {
		t.Errorf("Source = %q\nwant %q", set.Assets[0].Source, url)
	}

	rc, err := p.Fetch(context.Background(), set.Assets[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "PNGDATA" {
		t.Errorf("Fetch = %q", data)
	}
}

func TestPS2CoversUsesPS1Database(t *testing.T) {
	doer := &stubDoer{bodies: map[string]string{}}
	p, _ := provider.NewRegistry().New("ps2-covers", provider.Options{HTTP: doer})
	set, _ := p.Lookup(context.Background(),
		model.Game{Platform: model.PlatformPS1, GameID: "SLUS_000.67"},
		[]model.AssetType{model.AssetCover})
	if len(set.Assets) != 1 {
		t.Fatal("no PS1 cover looked up")
	}
	if !strings.Contains(set.Assets[0].Source, "psx-covers") {
		t.Errorf("PS1 cover came from %q, want the psx-covers database", set.Assets[0].Source)
	}
}

func TestProviderReportsMissingAsNotAvailable(t *testing.T) {
	doer := &stubDoer{bodies: map[string]string{}}
	p, _ := provider.NewRegistry().New("ps2-covers", provider.Options{HTTP: doer})
	_, err := p.Fetch(context.Background(), model.Asset{Source: "https://example.invalid/x.jpg"})
	if !errors.Is(err, provider.ErrNotAvailable) {
		t.Fatalf("err = %v, want ErrNotAvailable for a 404", err)
	}
}

func TestHTTPTemplateProvider(t *testing.T) {
	doer := &stubDoer{bodies: map[string]string{
		"https://art.example/ps2/SLUS-21050/BG.png": "bg",
	}}
	p, err := provider.NewRegistry().New("http", provider.Options{
		HTTP:      doer,
		Templates: map[string]string{"BG": "https://art.example/{platform}/{serial}/{type}.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	set, err := p.Lookup(context.Background(),
		model.Game{Platform: model.PlatformPS2, GameID: "SLUS_210.50"},
		[]model.AssetType{model.AssetCover, model.AssetBackground})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Assets) != 1 || set.Assets[0].Type != model.AssetBackground {
		t.Fatalf("Lookup = %+v", set.Assets)
	}
	if set.Assets[0].Source != "https://art.example/ps2/SLUS-21050/BG.png" {
		t.Errorf("Source = %q", set.Assets[0].Source)
	}
}

func TestHTTPTemplateRequiresTemplates(t *testing.T) {
	if _, err := provider.NewRegistry().New("http", provider.Options{}); err == nil {
		t.Fatal("the http provider was built with no templates")
	}
}

func TestLocalProvider(t *testing.T) {
	mirror := t.TempDir()
	art := filepath.Join(mirror, "ART")
	if err := os.MkdirAll(art, 0o755); err != nil {
		t.Fatal(err)
	}
	// A mirror copied straight out of a +OPL partition must work unchanged.
	if err := os.WriteFile(filepath.Join(art, "SLUS_210.50_BG.png"), []byte("bgdata"), 0o600); err != nil {
		t.Fatal(err)
	}
	// A flat mirror using the dashed serial must work too.
	if err := os.WriteFile(filepath.Join(mirror, "SLUS-21050_ICO.png"), []byte("ico"), 0o600); err != nil {
		t.Fatal(err)
	}

	p, err := provider.NewRegistry().New("local", provider.Options{Mirror: mirror})
	if err != nil {
		t.Fatal(err)
	}
	set, err := p.Lookup(context.Background(),
		model.Game{Platform: model.PlatformPS2, GameID: "SLUS_210.50"},
		[]model.AssetType{model.AssetBackground, model.AssetIcon, model.AssetCover})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Assets) != 2 {
		t.Fatalf("Lookup found %d assets, want 2: %+v", len(set.Assets), set.Assets)
	}
	rc, err := p.Fetch(context.Background(), set.Assets[0])
	if err != nil {
		t.Fatal(err)
	}
	data, _ := io.ReadAll(rc)
	rc.Close()
	if string(data) != "bgdata" {
		t.Errorf("Fetch = %q", data)
	}
}

func TestLocalProviderRefusesEscape(t *testing.T) {
	mirror := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	p, err := provider.NewRegistry().New("local", provider.Options{Mirror: mirror})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.Fetch(context.Background(), model.Asset{Source: outside}); err == nil {
		t.Fatal("the local provider read a file outside its mirror")
	}
}

func TestLocalProviderNeedsMirror(t *testing.T) {
	if _, err := provider.NewRegistry().New("local", provider.Options{}); err == nil {
		t.Fatal("the local provider was built with no mirror")
	}
	if _, err := provider.NewRegistry().New("local", provider.Options{Mirror: "/nonexistent"}); err == nil {
		t.Fatal("the local provider accepted a missing mirror")
	}
}

func TestUnknownProvider(t *testing.T) {
	_, err := provider.NewRegistry().New("nope", provider.Options{})
	if err == nil || !strings.Contains(err.Error(), "available:") {
		t.Fatalf("err = %v; an unknown provider should list the known ones", err)
	}
}

// A chain prefers the first provider that has a slot and falls through for the
// rest, which is how "local mirror, then network" is expressed.
func TestChainPrefersFirstProvider(t *testing.T) {
	mirror := t.TempDir()
	if err := os.WriteFile(filepath.Join(mirror, "SLUS_210.50_COV.png"), []byte("local-cover"), 0o600); err != nil {
		t.Fatal(err)
	}
	local, err := provider.NewRegistry().New("local", provider.Options{Mirror: mirror})
	if err != nil {
		t.Fatal(err)
	}
	doer := &stubDoer{bodies: map[string]string{
		"https://art.example/SLUS-21050_BG.png": "remote-bg",
	}}
	remote, err := provider.NewRegistry().New("http", provider.Options{
		HTTP: doer,
		Templates: map[string]string{
			"COV": "https://art.example/{serial}_COV.png",
			"BG":  "https://art.example/{serial}_BG.png",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	chain := provider.Chain{Providers: []provider.Provider{local, remote}}

	set, err := chain.Lookup(context.Background(),
		model.Game{Platform: model.PlatformPS2, GameID: "SLUS_210.50"},
		[]model.AssetType{model.AssetCover, model.AssetBackground})
	if err != nil {
		t.Fatal(err)
	}
	if len(set.Assets) != 2 {
		t.Fatalf("chain found %d assets: %+v", len(set.Assets), set.Assets)
	}
	byType := map[model.AssetType]string{}
	for _, a := range set.Assets {
		byType[a.Type] = a.Source
	}
	if !strings.HasPrefix(byType[model.AssetCover], mirror) {
		t.Errorf("cover came from %q, want the local mirror", byType[model.AssetCover])
	}
	if !strings.HasPrefix(byType[model.AssetBackground], "https://") {
		t.Errorf("background came from %q, want the remote provider", byType[model.AssetBackground])
	}
}
