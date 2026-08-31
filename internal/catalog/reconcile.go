package catalog

import (
	"sort"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// Entry is one row of the unified library: a title, where it can be found, and
// what artwork it is missing.
type CatalogEntry struct {
	model.Game
	AvailableInSource bool `json:"available_in_source"`
	// MissingAssets are enabled slots the provider could supply but which are
	// not on the HDD. These are the real gaps: syncing closes them.
	MissingAssets []model.AssetType `json:"missing_assets,omitempty"`
	// UnavailableAssets are enabled slots the configured provider cannot
	// supply for anyone. They are absent from the HDD too, but no amount of
	// syncing will change that, so they are not counted as missing artwork.
	UnavailableAssets []model.AssetType `json:"unavailable_assets,omitempty"`
	// AssetsKnown records that the artwork inventory was actually read. When
	// it is false an empty MissingAssets means "not checked", not "complete":
	// reading +OPL needs pfsfuse, which may not be installed, and reporting a
	// game as having complete artwork without having looked would be a
	// confident false claim.
	AssetsKnown bool `json:"assets_known"`
	// SourceGame carries the source-side view when a title is both installed
	// and available, so the details view can show the image path alongside the
	// installed partition.
	SourceGame *model.Game `json:"source_game,omitempty"`
}

// State is the coarse status shown in the library list.
type State string

const (
	StateInstalled          State = "installed"
	StateAvailable          State = "available"
	StateInstalledAndSource State = "installed+source"
)

// State classifies an entry.
func (e CatalogEntry) State() State {
	switch {
	case e.Installed && e.AvailableInSource:
		return StateInstalledAndSource
	case e.Installed:
		return StateInstalled
	default:
		return StateAvailable
	}
}

// StatusText is the short label the library table shows.
func (e CatalogEntry) StatusText() string {
	if !e.Installed {
		return "Available"
	}
	if len(e.MissingAssets) > 0 {
		return "Installed"
	}
	return "Installed"
}

// Catalog is the reconciled library.
type Catalog struct {
	Entries []CatalogEntry `json:"entries"`
	// Problems carries the source files that could not be identified, so the
	// TUI can show them rather than leaving a user wondering.
	Problems []ScanProblem `json:"problems,omitempty"`
}

// Reconcile merges the installed library with the source listings.
//
// Source directories are browsing locations, never a record of installed
// state: an entry is Installed only because the HDD said so. Matching is on
// the normalised game id, so the same title found as "SLUS-21050.iso" in a
// source directory and as "SLUS_210.50" on the HDD is one row.
func Reconcile(installed, source []model.Game, problems []ScanProblem) Catalog {
	byKey := map[string]int{}
	c := Catalog{Problems: problems}

	for _, g := range installed {
		g.Installed = true
		c.Entries = append(c.Entries, CatalogEntry{Game: g})
		byKey[g.Key()] = len(c.Entries) - 1
	}

	for _, g := range source {
		g.Installed = false
		if i, ok := byKey[g.Key()]; ok {
			e := &c.Entries[i]
			e.AvailableInSource = true
			// The installed record has no source path; borrow it so the
			// details view can show where the image came from.
			if e.SourcePath == "" {
				e.SourcePath = g.SourcePath
			}
			src := g
			e.SourceGame = &src
			continue
		}
		c.Entries = append(c.Entries, CatalogEntry{Game: g, AvailableInSource: true})
		byKey[g.Key()] = len(c.Entries) - 1
	}

	sort.SliceStable(c.Entries, func(i, j int) bool {
		a, b := c.Entries[i], c.Entries[j]
		if a.Platform != b.Platform {
			return a.Platform < b.Platform
		}
		ta, tb := strings.ToLower(a.Title), strings.ToLower(b.Title)
		if ta != tb {
			return ta < tb
		}
		return a.GameID < b.GameID
	})
	return c
}

// Filter narrows a catalog.
type Filter struct {
	// Platform limits to one platform; empty means both.
	Platform model.Platform
	// Installed and NotInstalled are mutually exclusive when both set, which
	// yields nothing; the TUI never sets both.
	Installed    bool
	NotInstalled bool
	MissingAsset bool
	MultiDisc    bool
	// Search is a case-insensitive substring matched against the title and the
	// game id.
	Search string
}

// Empty reports whether the filter would let everything through.
func (f Filter) Empty() bool {
	return f.Platform == "" && !f.Installed && !f.NotInstalled &&
		!f.MissingAsset && !f.MultiDisc && f.Search == ""
}

// Match reports whether an entry passes the filter.
func (f Filter) Match(e CatalogEntry) bool {
	if f.Platform != "" && e.Platform != f.Platform {
		return false
	}
	if f.Installed && !e.Installed {
		return false
	}
	if f.NotInstalled && e.Installed {
		return false
	}
	// An unchecked entry cannot be said to be missing artwork, so it does not
	// match a filter that asks for exactly that.
	if f.MissingAsset && (!e.AssetsKnown || len(e.MissingAssets) == 0) {
		return false
	}
	if f.MultiDisc && !e.IsMultiDisc() {
		return false
	}
	if f.Search != "" {
		needle := strings.ToLower(f.Search)
		hay := strings.ToLower(e.Title + " " + e.GameID)
		if !strings.Contains(hay, needle) {
			// A search for "slus 21050" should still find "SLUS_210.50".
			if !strings.Contains(model.NormalizeGameID(e.GameID), model.NormalizeGameID(f.Search)) ||
				model.NormalizeGameID(f.Search) == "" {
				return false
			}
		}
	}
	return true
}

// Apply returns the entries that pass the filter.
func (c Catalog) Apply(f Filter) []CatalogEntry {
	if f.Empty() {
		return c.Entries
	}
	out := make([]CatalogEntry, 0, len(c.Entries))
	for _, e := range c.Entries {
		if f.Match(e) {
			out = append(out, e)
		}
	}
	return out
}

// Find returns the entry matching a game id or an exact title, and reports
// whether the search was ambiguous.
//
// Ambiguity is surfaced rather than resolved: `ps2hdd remove "Final Fantasy"`
// must not delete a title the user did not name.
func (c Catalog) Find(query string) (matches []CatalogEntry) {
	norm := model.NormalizeGameID(query)
	lower := strings.ToLower(strings.TrimSpace(query))
	for _, e := range c.Entries {
		if norm != "" && model.NormalizeGameID(e.GameID) == norm {
			return []CatalogEntry{e}
		}
		if strings.ToLower(e.Title) == lower {
			matches = append(matches, e)
		}
	}
	if len(matches) > 0 {
		return matches
	}
	// Fall back to a substring match so partial titles work interactively.
	for _, e := range c.Entries {
		if lower != "" && strings.Contains(strings.ToLower(e.Title), lower) {
			matches = append(matches, e)
		}
	}
	return matches
}

// Counts summarises a catalog for the status line.
func (c Catalog) Counts() (installed, available, missingAssets int) {
	return Count(c.Entries)
}

// Count totals a set of entries.
//
// It takes the entries rather than reading them off the catalog because a
// summary printed under a filtered listing has to describe what was shown. A
// count of the whole catalog under `list --installed` reports the hundreds of
// titles the filter just excluded, which reads as the filter having done
// nothing at all.
func Count(entries []CatalogEntry) (installed, available, missingAssets int) {
	for _, e := range entries {
		if e.Installed {
			installed++
		} else {
			available++
		}
		if e.AssetsKnown && len(e.MissingAssets) > 0 {
			missingAssets++
		}
	}
	return
}
