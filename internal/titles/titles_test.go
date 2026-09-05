package titles_test

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/titles"
)

// fakeHTTP serves canned CFG bodies and counts requests, so the cache can be
// shown to actually prevent a second one.
type fakeHTTP struct {
	bodies map[string]string // url substring -> body, "" means 404
	calls  int
	err    error
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	for frag, body := range f.bodies {
		if strings.Contains(req.URL.String(), frag) {
			if body == "" {
				break
			}
			return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
		}
	}
	return &http.Response{StatusCode: 404, Body: io.NopCloser(strings.NewReader(""))}, nil
}

func TestNormalize(t *testing.T) {
	cases := map[string]string{
		// Redump moves the article to the end so titles sort alphabetically.
		// That is a sorting convention, not the name of the game.
		"King of Fighters '99, The": "The King of Fighters '99",
		"Legend of Dragoon, A":      "A Legend of Dragoon",
		// ps2hdd appends its own _CD2 from the disc number it worked out, so
		// carrying Redump's marker as well would name a file "... (Disc 2)_CD2".
		"Final Fantasy VII (Disc 1)": "Final Fantasy VII",
		"Metal Gear Solid (Disc 2)":  "Metal Gear Solid",
		"Parasite Eve II (Disc 1)":   "Parasite Eve II",
		// A tag that is not a disc marker is part of the name and stays.
		"Point Blank (Demo)": "Point Blank (Demo)",
		// Everything else is left exactly as the database has it.
		"Marvel vs. Capcom - Clash of Super Heroes": "Marvel vs. Capcom - Clash of Super Heroes",
		"Spyro the Dragon":                          "Spyro the Dragon",
		"  spaced   out  ":                          "spaced out",
	}
	for in, want := range cases {
		if got := titles.Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestLookupReadsTheTitleField(t *testing.T) {
	f := &fakeHTTP{bodies: map[string]string{
		"SCUS_942.28": "CfgVersion=8\nTitle=Spyro the Dragon\nGenre=Platformer\n",
	}}
	l := titles.Open(t.TempDir())
	l.HTTP = f

	got, ok := l.Title(context.Background(), model.PlatformPS1, "SCUS_942.28")
	if !ok || got != "Spyro the Dragon" {
		t.Fatalf("got %q ok=%v", got, ok)
	}
	// A second lookup must be served from memory. A library of two thousand
	// games would otherwise be two thousand requests every run.
	if _, _ = l.Title(context.Background(), model.PlatformPS1, "SCUS_942.28"); f.calls != 1 {
		t.Errorf("made %d requests for one serial, want 1", f.calls)
	}
	// The serial's punctuation must not matter: the same disc spelled three
	// ways is one lookup.
	if _, _ = l.Title(context.Background(), model.PlatformPS1, "SCUS-94228"); f.calls != 1 {
		t.Errorf("a differently punctuated serial missed the cache: %d requests", f.calls)
	}
}

// A serial the database has never heard of is remembered as absent, so a
// library full of homebrew does not re-ask on every run.
func TestLookupRemembersAbsence(t *testing.T) {
	f := &fakeHTTP{bodies: map[string]string{}}
	l := titles.Open(t.TempDir())
	l.HTTP = f

	if _, ok := l.Title(context.Background(), model.PlatformPS1, "SLUS_999.99"); ok {
		t.Fatal("an unknown serial reported a title")
	}
	_, _ = l.Title(context.Background(), model.PlatformPS1, "SLUS_999.99")
	if f.calls != 1 {
		t.Errorf("asked %d times about a serial known to be absent, want 1", f.calls)
	}
}

// A transport failure must NOT be remembered: the next run may have a network,
// and caching "no" would make one offline moment permanent.
func TestLookupDoesNotCacheATransportFailure(t *testing.T) {
	f := &fakeHTTP{err: fmt.Errorf("no route to host")}
	l := titles.Open(t.TempDir())
	l.HTTP = f

	if _, ok := l.Title(context.Background(), model.PlatformPS1, "SCUS_942.28"); ok {
		t.Fatal("a failed lookup reported a title")
	}
	f.err = nil
	f.bodies = map[string]string{"SCUS_942.28": "Title=Spyro the Dragon\n"}
	got, ok := l.Title(context.Background(), model.PlatformPS1, "SCUS_942.28")
	if !ok || got != "Spyro the Dragon" {
		t.Errorf("a lookup that failed once never succeeded: %q ok=%v", got, ok)
	}
}

// The cache survives the process, which is the point of writing it down.
func TestLookupCacheRoundTrips(t *testing.T) {
	dir := t.TempDir()
	f := &fakeHTTP{bodies: map[string]string{"SLUS_010.13": "Title=Legend of Mana\n"}}
	l := titles.Open(dir)
	l.HTTP = f
	if _, ok := l.Title(context.Background(), model.PlatformPS1, "SLUS_010.13"); !ok {
		t.Fatal("first lookup failed")
	}
	if err := l.Save(); err != nil {
		t.Fatalf("Save: %v", err)
	}

	second := titles.Open(dir)
	second.HTTP = &fakeHTTP{err: fmt.Errorf("should not be called")}
	got, ok := second.Title(context.Background(), model.PlatformPS1, "SLUS_010.13")
	if !ok || got != "Legend of Mana" {
		t.Errorf("a new process did not reuse the cache: %q ok=%v", got, ok)
	}
}

// Offline is a supported state, not an error.
func TestOfflineLookupAnswersNothing(t *testing.T) {
	l := titles.NewOffline()
	if _, ok := l.Title(context.Background(), model.PlatformPS1, "SCUS_942.28"); ok {
		t.Error("an offline lookup invented a title")
	}
	if err := l.Save(); err != nil {
		t.Errorf("Save on an offline lookup: %v", err)
	}
}

// The two platforms come from different databases and must not be confused.
func TestLookupKeysOnPlatform(t *testing.T) {
	f := &fakeHTTP{bodies: map[string]string{
		"PS1-OPL-CFG-Database": "Title=A PS1 Game\n",
		"PS2-OPL-CFG-Database": "Title=A PS2 Game\n",
	}}
	l := titles.Open(t.TempDir())
	l.HTTP = f
	ps1Title, _ := l.Title(context.Background(), model.PlatformPS1, "SLUS_000.01")
	ps2Title, _ := l.Title(context.Background(), model.PlatformPS2, "SLUS_000.01")
	if ps1Title != "A PS1 Game" || ps2Title != "A PS2 Game" {
		t.Errorf("ps1=%q ps2=%q: the same serial on two platforms must not share an answer", ps1Title, ps2Title)
	}
}
