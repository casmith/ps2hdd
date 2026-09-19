// Package homebrew keeps ps2hdd's knowledge of the PS2 homebrew a user runs
// alongside their games, and of where to get the current version of it.
//
// OPL is the reason this exists. It is the program the console spends most of
// its time in, it is released as a rolling build rather than a numbered one,
// and the version a drive is carrying is otherwise invisible: the ELF has no
// readable version string, so the only reliable record of what was installed
// is the one ps2hdd writes down when it installs it.
package homebrew

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"time"
)

// Doer is the subset of http.Client used here, so tests need no network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// App is a homebrew program ps2hdd knows how to fetch.
type App struct {
	// ID is what a user types: "opl".
	ID string
	// Name is what a menu shows.
	Name string
	// Description says what it is, for a listing.
	Description string
	// Repo is the GitHub "owner/name" the releases come from.
	Repo string
	// Tag is the release to read. OPL publishes a rolling "latest"
	// prerelease that is updated in place, so there is no version to follow.
	Tag string
	// Asset is the file to download: the bare ELF, not an archive, so that
	// installing needs no unpacking step on the way to the HDD.
	Asset string
	// ELFName is what the file is called once installed.
	ELFName string
	// VersionFrom extracts a version from an asset filename, capturing it in
	// group 1. A rolling release carries no version of its own -- OPL's tag
	// is literally "latest" -- and the only place the build number appears is
	// in the names of the archives published beside the ELF.
	VersionFrom *regexp.Regexp
}

// Catalog is the homebrew ps2hdd can install and update.
//
// It is deliberately short. Every entry has to publish a bare ELF against a
// stable release URL; wLaunchELF, for instance, tags releases but attaches no
// files to them, so there is nothing to fetch and it is better to say so than
// to scrape a forum post.
var Catalog = []App{
	{
		ID:          "opl",
		Name:        "Open PS2 Loader",
		Description: "the loader that lists and launches the games on the drive",
		Repo:        "ps2homebrew/Open-PS2-Loader",
		Tag:         "latest",
		Asset:       "OPNPS2LD.ELF",
		ELFName:     "OPNPS2LD.ELF",
		VersionFrom: regexp.MustCompile(`^OPNPS2LD-(v[0-9][^/]*)\.7z$`),
	},
}

// Find returns the catalogued app with an ID.
func Find(id string) (App, bool) {
	for _, a := range Catalog {
		if a.ID == id {
			return a, true
		}
	}
	return App{}, false
}

// Release is what upstream is currently offering.
type Release struct {
	// Version is the build, e.g. "v1.2.0-Beta-2245-3e3f34e".
	Version string
	// URL is where the ELF can be downloaded.
	URL string
	// Size is the ELF's size in bytes.
	Size int64
	// Published is the date the release was last updated.
	Published time.Time
}

// ghRelease is the subset of GitHub's release JSON that matters here.
type ghRelease struct {
	TagName     string    `json:"tag_name"`
	PublishedAt time.Time `json:"published_at"`
	Assets      []struct {
		Name string `json:"name"`
		Size int64  `json:"size"`
		URL  string `json:"browser_download_url"`
	} `json:"assets"`
}

// APIBase is where releases are read from. It is a variable so a test can
// point it at its own server.
var APIBase = "https://api.github.com"

// Latest reports what upstream currently offers for an app.
func Latest(ctx context.Context, client Doer, a App) (Release, error) {
	url := fmt.Sprintf("%s/repos/%s/releases/tags/%s", APIBase, a.Repo, a.Tag)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Release{}, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	resp, err := client.Do(req)
	if err != nil {
		return Release{}, fmt.Errorf("read the %s release: %w", a.Name, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("read the %s release: %s", a.Name, resp.Status)
	}
	var gr ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&gr); err != nil {
		return Release{}, fmt.Errorf("read the %s release: %w", a.Name, err)
	}

	rel := Release{Published: gr.PublishedAt}
	for _, as := range gr.Assets {
		if as.Name == a.Asset {
			rel.URL, rel.Size = as.URL, as.Size
		}
		if a.VersionFrom != nil && rel.Version == "" {
			if m := a.VersionFrom.FindStringSubmatch(as.Name); m != nil {
				rel.Version = m[1]
			}
		}
	}
	if rel.URL == "" {
		return Release{}, fmt.Errorf("the %s release has no %s to download", a.Name, a.Asset)
	}
	if rel.Version == "" {
		// A build with no version is still installable, but nothing could
		// then say whether it is newer than what is on the drive, and a
		// listing that claims "up to date" without knowing would be worse
		// than one that admits it does not.
		rel.Version = gr.TagName
	}
	return rel, nil
}

// ManifestFile is where ps2hdd records what it installed, in the root of the
// OPL partition.
//
// A record is necessary rather than tidy. These ELFs carry no version string
// that can be read back -- POPSTARTER.ELF has "Revision" in it and no number
// beside it, and OPNPS2LD.ELF is a rolling build whose only version lives in
// the filename of an archive published next to it. Without writing down what
// was installed there is no way to answer "is this current?" short of
// downloading the candidate and comparing bytes.
const ManifestFile = "ps2hdd-apps.json"

// Record is one installed app.
type Record struct {
	Version   string    `json:"version"`
	ELF       string    `json:"elf"`
	Installed time.Time `json:"installed"`
}

// Manifest is what ps2hdd has installed on a drive.
type Manifest struct {
	Version int               `json:"version"`
	Apps    map[string]Record `json:"apps"`
}

const manifestVersion = 1

// ReadManifest loads the record from a mounted OPL partition. A drive with no
// manifest is not an error: it is a drive nothing has been installed on yet,
// or one set up by another tool.
func ReadManifest(oplMount string) Manifest {
	m := Manifest{Version: manifestVersion, Apps: map[string]Record{}}
	data, err := readFile(oplMount)
	if err != nil {
		return m
	}
	var got Manifest
	if err := json.Unmarshal(data, &got); err != nil || got.Version != manifestVersion {
		return m
	}
	if got.Apps != nil {
		m.Apps = got.Apps
	}
	return m
}

// readFile is separated so ReadManifest reads exactly one path and the test
// for a missing manifest is about the missing case, not about path building.
func readFile(oplMount string) ([]byte, error) {
	return os.ReadFile(filepath.Join(oplMount, ManifestFile))
}

// WriteManifest records what is installed, in the root of a mounted OPL
// partition.
func WriteManifest(oplMount string, m Manifest) error {
	m.Version = manifestVersion
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(oplMount, ManifestFile), append(data, '\n'), 0o644)
}

// Status is what a listing shows for one app.
type Status struct {
	App App
	// Installed is the recorded version, empty when the app is not installed
	// or was put there by something else.
	Installed string
	// Present is whether the ELF is actually on the drive, which is not the
	// same as having a record of it: a manifest entry whose file is gone is a
	// broken install, not an installed app.
	Present bool
	// Latest is what upstream offers, empty when it could not be read.
	Latest Release
	// Err is why Latest is empty.
	Err error
}

// UpdateAvailable reports whether there is a newer build to install.
//
// An app that is not installed is not "out of date", and an app whose upstream
// could not be read is not either: both are reported as what they are rather
// than folded into a number that would be wrong.
func (s Status) UpdateAvailable() bool {
	if !s.Present || s.Latest.Version == "" || s.Installed == "" {
		return false
	}
	return s.Installed != s.Latest.Version
}
