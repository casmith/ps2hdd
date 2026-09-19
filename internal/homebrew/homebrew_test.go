package homebrew_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/homebrew"
)

// oplRelease is the shape GitHub actually returns for OPL's rolling tag: the
// release itself is called "latest" and carries no version, and the build
// number appears only in the names of the archives beside the ELF.
const oplRelease = `{
  "tag_name": "latest",
  "published_at": "2026-09-03T00:00:00Z",
  "assets": [
    {"name": "OPNPS2LD-LANGS-v1.2.0-Beta-2245-3e3f34e.7z", "size": 3395609,
     "browser_download_url": "https://example.invalid/langs.7z"},
    {"name": "OPNPS2LD-v1.2.0-Beta-2245-3e3f34e.7z", "size": 1404696,
     "browser_download_url": "https://example.invalid/opl.7z"},
    {"name": "OPNPS2LD.ELF", "size": 1360836,
     "browser_download_url": "https://example.invalid/OPNPS2LD.ELF"}
  ]
}`

type fakeHTTP struct {
	body   string
	status int
	err    error
	calls  int
}

func (f *fakeHTTP) Do(req *http.Request) (*http.Response, error) {
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	st := f.status
	if st == 0 {
		st = 200
	}
	return &http.Response{StatusCode: st, Status: fmt.Sprintf("%d", st),
		Body: io.NopCloser(strings.NewReader(f.body))}, nil
}

func oplApp(t *testing.T) homebrew.App {
	t.Helper()
	a, ok := homebrew.Find("opl")
	if !ok {
		t.Fatal("opl is not in the catalog")
	}
	return a
}

// The version has to be dug out of an asset filename, because the release is a
// rolling tag literally named "latest".
func TestLatestReadsTheVersionFromAnAssetName(t *testing.T) {
	f := &fakeHTTP{body: oplRelease}
	rel, err := homebrew.Latest(context.Background(), f, oplApp(t))
	if err != nil {
		t.Fatalf("Latest: %v", err)
	}
	if rel.Version != "v1.2.0-Beta-2245-3e3f34e" {
		t.Errorf("Version = %q, want the build from the archive's filename", rel.Version)
	}
	// The bare ELF is what gets installed -- not the .7z beside it, which
	// would need unpacking on the way to the HDD.
	if !strings.HasSuffix(rel.URL, "OPNPS2LD.ELF") {
		t.Errorf("URL = %q, want the bare ELF", rel.URL)
	}
	if rel.Size != 1360836 {
		t.Errorf("Size = %d, want the ELF's size and not an archive's", rel.Size)
	}
}

// A release with no ELF is not installable, and saying so beats installing an
// archive that the console cannot run.
func TestLatestRefusesAReleaseWithoutTheAsset(t *testing.T) {
	f := &fakeHTTP{body: `{"tag_name":"latest","assets":[
	  {"name":"OPNPS2LD-v9.9.9.7z","size":1,"browser_download_url":"https://example.invalid/x.7z"}]}`}
	_, err := homebrew.Latest(context.Background(), f, oplApp(t))
	if err == nil {
		t.Fatal("a release with no ELF was accepted")
	}
	if !strings.Contains(err.Error(), "OPNPS2LD.ELF") {
		t.Errorf("the error does not name what is missing: %v", err)
	}
}

// When no asset carries a version, the tag is used rather than leaving it
// blank -- but it must never be silently reported as up to date.
func TestLatestFallsBackToTheTag(t *testing.T) {
	f := &fakeHTTP{body: `{"tag_name":"latest","assets":[
	  {"name":"OPNPS2LD.ELF","size":10,"browser_download_url":"https://example.invalid/e"}]}`}
	rel, err := homebrew.Latest(context.Background(), f, oplApp(t))
	if err != nil {
		t.Fatal(err)
	}
	if rel.Version != "latest" {
		t.Errorf("Version = %q, want the tag as a last resort", rel.Version)
	}
}

func TestLatestReportsATransportFailure(t *testing.T) {
	f := &fakeHTTP{err: fmt.Errorf("no route to host")}
	if _, err := homebrew.Latest(context.Background(), f, oplApp(t)); err == nil {
		t.Fatal("a failed request reported a release")
	}
	f2 := &fakeHTTP{body: "{}", status: 404}
	if _, err := homebrew.Latest(context.Background(), f2, oplApp(t)); err == nil {
		t.Fatal("a 404 reported a release")
	}
}

// A drive with no manifest is a drive nothing has been installed on, not an
// error.
func TestReadManifestOfAnUntouchedDrive(t *testing.T) {
	m := homebrew.ReadManifest(t.TempDir())
	if len(m.Apps) != 0 {
		t.Errorf("apps = %v, want none", m.Apps)
	}
}

func TestManifestRoundTrips(t *testing.T) {
	dir := t.TempDir()
	m := homebrew.ReadManifest(dir)
	m.Apps["opl"] = homebrew.Record{Version: "v1.2.0-Beta-2245-3e3f34e", ELF: "OPNPS2LD.ELF"}
	if err := homebrew.WriteManifest(dir, m); err != nil {
		t.Fatalf("WriteManifest: %v", err)
	}
	back := homebrew.ReadManifest(dir)
	if back.Apps["opl"].Version != "v1.2.0-Beta-2245-3e3f34e" {
		t.Errorf("round trip lost the version: %+v", back)
	}
	// It has to be readable by something other than ps2hdd; a user looking at
	// their own drive should be able to see what is on it.
	data, err := os.ReadFile(filepath.Join(dir, homebrew.ManifestFile))
	if err != nil {
		t.Fatal(err)
	}
	var any map[string]any
	if err := json.Unmarshal(data, &any); err != nil {
		t.Errorf("the manifest is not valid JSON: %v", err)
	}
}

// The three states a listing has to tell apart. Folding any of them into "up
// to date" or "out of date" would be a lie.
func TestUpdateAvailableDistinguishesTheUnknownCases(t *testing.T) {
	a := oplApp(t)
	newer := homebrew.Release{Version: "v2"}
	cases := map[string]struct {
		s    homebrew.Status
		want bool
	}{
		"newer build available":     {homebrew.Status{App: a, Present: true, Installed: "v1", Latest: newer}, true},
		"already current":           {homebrew.Status{App: a, Present: true, Installed: "v2", Latest: newer}, false},
		"not installed at all":      {homebrew.Status{App: a, Present: false, Latest: newer}, false},
		"installed by another tool": {homebrew.Status{App: a, Present: true, Installed: "", Latest: newer}, false},
		"upstream unreachable":      {homebrew.Status{App: a, Present: true, Installed: "v1"}, false},
	}
	for name, tc := range cases {
		if got := tc.s.UpdateAvailable(); got != tc.want {
			t.Errorf("%s: UpdateAvailable() = %v, want %v", name, got, tc.want)
		}
	}
}

// Every catalogued app must be fetchable: a stable release URL and a bare ELF.
// An entry that fails this is one a user would be told about and then unable
// to install.
func TestCatalogEntriesAreComplete(t *testing.T) {
	for _, a := range homebrew.Catalog {
		if a.ID == "" || a.Name == "" || a.Repo == "" || a.Tag == "" || a.Asset == "" || a.ELFName == "" {
			t.Errorf("incomplete catalog entry: %+v", a)
		}
		if !strings.Contains(a.Repo, "/") {
			t.Errorf("%s: Repo = %q, want owner/name", a.ID, a.Repo)
		}
	}
}
