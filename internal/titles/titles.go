// Package titles resolves a game's real name from the serial printed on its
// disc.
//
// A filename is whatever whoever made the rip felt like typing:
// "hot-shots-golf-u-scus-94188", "SLUS_00067", "Disc 1". The serial inside the
// image is not -- it is stamped on the disc and read out of SYSTEM.CNF -- so
// it is the one thing that can be turned into the name a person would
// recognise. That name ends up in OPL's menu, in the VCD's filename and in the
// launcher's, so getting it from the disc rather than the filesystem is worth
// a network round trip.
//
// The data is the OPL CFG databases, which are generated from Redump's
// datfiles and keyed by the same OPL serial form ps2hdd uses everywhere else.
// Lookups are cached, so a library is only ever asked about once.
package titles

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/casmith/ps2hdd/internal/model"
)

// Doer is the subset of http.Client used here, so tests need no network.
type Doer interface {
	Do(req *http.Request) (*http.Response, error)
}

// sources maps a platform to the CFG database that covers it. Both are
// generated from Redump and both spell the serial the OPL way.
var sources = map[model.Platform]string{
	model.PlatformPS1: "https://raw.githubusercontent.com/BeardedKraken13/PS1-OPL-CFG-Database/master/CFG_FINAL/%s.cfg",
	model.PlatformPS2: "https://raw.githubusercontent.com/Tom-Bruise/PS2-OPL-CFG-Database/master/CFG_en/%s.cfg",
}

// Lookup resolves serials to titles, remembering what it learns.
type Lookup struct {
	// HTTP is the client used for lookups. A nil client means offline.
	HTTP Doer

	mu    sync.Mutex
	known map[string]string // "ps1|SLUS_000.67" -> title, "" when the database has no entry
	path  string
	dirty bool
}

// cacheFile is the on-disk shape.
type cacheFile struct {
	Version int               `json:"version"`
	Titles  map[string]string `json:"titles"`
}

const cacheVersion = 1

// Open loads the cache from dir. A cache that cannot be read is not an error:
// it degrades to asking the network again, never to a failed install.
func Open(dir string) *Lookup {
	l := &Lookup{
		known: map[string]string{},
		path:  filepath.Join(dir, "titles.json"),
		HTTP:  &http.Client{Timeout: 15 * time.Second},
	}
	data, err := os.ReadFile(l.path)
	if err != nil {
		return l
	}
	var f cacheFile
	if err := json.Unmarshal(data, &f); err != nil || f.Version != cacheVersion {
		return l
	}
	if f.Titles != nil {
		l.known = f.Titles
	}
	return l
}

// NewOffline returns a lookup that never touches the network, for tests and
// for a caller that has turned canonical titles off.
func NewOffline() *Lookup {
	return &Lookup{known: map[string]string{}}
}

func key(p model.Platform, serial string) string {
	return string(p) + "|" + model.OPLGameID(serial)
}

// Title returns the canonical title for a serial, and whether one was found.
//
// A serial the database does not carry is remembered as absent, so a library
// full of homebrew or bad dumps does not re-ask on every run.
func (l *Lookup) Title(ctx context.Context, platform model.Platform, serial string) (string, bool) {
	if serial == "" {
		return "", false
	}
	k := key(platform, serial)

	l.mu.Lock()
	cached, seen := l.known[k]
	l.mu.Unlock()
	if seen {
		return cached, cached != ""
	}
	if l.HTTP == nil {
		return "", false
	}
	tmpl, ok := sources[platform]
	if !ok {
		return "", false
	}

	title, err := l.fetch(ctx, fmt.Sprintf(tmpl, model.OPLGameID(serial)))
	if err != nil {
		// A failed lookup is not remembered: the next run may have a network.
		return "", false
	}
	title = Normalize(title)

	l.mu.Lock()
	l.known[k] = title
	l.dirty = true
	l.mu.Unlock()
	return title, title != ""
}

// fetch reads one CFG and returns its Title field, or "" when the database has
// no entry. Only a transport failure is an error.
func (l *Lookup) fetch(ctx context.Context, url string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	resp, err := l.HTTP.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return "", nil // no entry, which is an answer
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("%s: %s", url, resp.Status)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 8*1024), 256*1024)
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), "=")
		if ok && strings.EqualFold(strings.TrimSpace(k), "Title") {
			return strings.TrimSpace(v), nil
		}
	}
	return "", sc.Err()
}

// Save writes the cache back if anything was learned.
func (l *Lookup) Save() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if !l.dirty || l.path == "" {
		return nil
	}
	data, err := json.Marshal(cacheFile{Version: cacheVersion, Titles: l.known})
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(l.path), 0o700); err != nil {
		return err
	}
	tmp := l.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	if err := os.Rename(tmp, l.path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	l.dirty = false
	return nil
}

// discTagRe matches the disc marker Redump appends. ps2hdd adds its own "_CD2"
// from the disc number it worked out, so carrying Redump's as well would name
// a file "... (Disc 2)_CD2".
var discTagRe = regexp.MustCompile(`(?i)\s*\((?:disc|disk|cd)\s*\d+\)\s*$`)

// articleRe matches the trailing article Redump moves to the end so that titles
// sort alphabetically: "King of Fighters '99, The". That is a sorting
// convention, not a name, and nobody wants to read it on a menu.
var articleRe = regexp.MustCompile(`,\s*(The|A|An|Le|La|Les|Der|Die|Das|El|Los)$`)

// Normalize turns a database title into the one to display and to name files
// with.
func Normalize(s string) string {
	s = strings.TrimSpace(s)
	s = discTagRe.ReplaceAllString(s, "")
	if m := articleRe.FindStringSubmatch(s); m != nil {
		s = m[1] + " " + strings.TrimSpace(articleRe.ReplaceAllString(s, ""))
	}
	return strings.Join(strings.Fields(s), " ")
}
