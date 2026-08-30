package ps1

import (
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// discTagRe matches the disc markers release groups use in filenames:
// "(Disc 2)", "[CD3]", " - Disc 1 of 3", "(Disk 2)". The number is captured.
var discTagRe = regexp.MustCompile(`(?i)[\(\[]?\s*(?:disc|disk|cd)\s*[ ._-]?\s*(\d{1,2})\s*(?:of\s*\d{1,2}\s*)?[\)\]]?`)

// trailingTagRe strips the region, revision and language tags that follow a
// title, so that "Final Fantasy VII (USA) (Disc 1)" and
// "Final Fantasy VII (USA) (Disc 2)" reduce to the same base title.
var trailingTagRe = regexp.MustCompile(`\s*[\(\[][^\)\]]*[\)\]]`)

// DiscNumber extracts the disc number from a filename, or 0 when the name
// carries no disc marker.
func DiscNumber(name string) int {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	m := discTagRe.FindStringSubmatch(base)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n <= 0 || n > 99 {
		return 0
	}
	return n
}

// BaseTitle reduces a filename to the title shared by every disc of a release.
func BaseTitle(name string) string {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base = discTagRe.ReplaceAllString(base, " ")
	base = trailingTagRe.ReplaceAllString(base, " ")
	base = strings.NewReplacer("_", " ").Replace(base)
	base = strings.Trim(strings.Join(strings.Fields(base), " "), " -_.")
	return base
}

// Group collects inspected discs into logical titles.
//
// Grouping is by directory first and base title second, because both layouts
// are common and both are unambiguous:
//
//	Final Fantasy VII/          one directory per title
//	  Disc 1.cue
//	  Disc 2.cue
//
//	psx/                        one flat directory
//	  Final Fantasy VII (Disc 1).cue
//	  Final Fantasy VII (Disc 2).cue
//
// Serials are deliberately *not* used to group: the discs of one release
// usually carry different serials, so grouping by serial would split every
// multi-disc title. The first disc's serial becomes the title's identity,
// which is what POPStarter and OPL key artwork off.
func Group(discs []Disc, sourceRoot string) []model.Game {
	type key struct{ dir, title string }
	order := []key{}
	buckets := map[key][]Disc{}

	for _, d := range discs {
		path := d.SourcePath()
		dir := filepath.Dir(path)
		title := BaseTitle(filepath.Base(path))
		// A directory that exists solely to hold one title's discs names the
		// title better than files called "Disc 1.cue" do.
		if dir != sourceRoot && titleIsUninformative(title) {
			title = BaseTitle(filepath.Base(dir))
		}
		k := key{dir: dir, title: strings.ToLower(title)}
		if _, seen := buckets[k]; !seen {
			order = append(order, k)
		}
		buckets[k] = append(buckets[k], d)
	}

	var out []model.Game
	for _, k := range order {
		group := buckets[k]
		sort.SliceStable(group, func(i, j int) bool {
			ni, nj := group[i].DiscNumber, group[j].DiscNumber
			if ni != nj {
				// Discs with no number sort after numbered ones.
				if ni == 0 {
					return false
				}
				if nj == 0 {
					return true
				}
				return ni < nj
			}
			return group[i].SourcePath() < group[j].SourcePath()
		})

		g := model.Game{
			Platform:      model.PlatformPS1,
			GameID:        group[0].GameID,
			SourcePath:    group[0].SourcePath(),
			ArchiveMember: group[0].ArchiveMember,
		}
		g.Title = displayTitle(group, k.dir, sourceRoot)
		for i, d := range group {
			n := d.DiscNumber
			if n == 0 {
				n = i + 1
			}
			g.Discs = append(g.Discs, model.Disc{
				Number:        n,
				ArchiveMember: d.ArchiveMember,
				GameID:        d.GameID,
				Title:         d.Title,
				SourcePath:    d.SourcePath(),
				SizeBytes:     d.SizeBytes,
			})
			g.SizeBytes += d.SizeBytes
		}
		out = append(out, g)
	}
	return out
}

// displayTitle picks the name to show for a group.
func displayTitle(group []Disc, dir, sourceRoot string) string {
	title := BaseTitle(filepath.Base(group[0].SourcePath()))
	if titleIsUninformative(title) && dir != sourceRoot {
		title = BaseTitle(filepath.Base(dir))
	}
	if title == "" {
		title = group[0].VolumeID
	}
	if title == "" {
		title = group[0].GameID
	}
	return title
}

// titleIsUninformative reports whether a filename-derived title says nothing
// beyond "this is a disc", which is the case for names like "Disc 1.cue".
func titleIsUninformative(title string) bool {
	t := strings.ToLower(strings.TrimSpace(title))
	if t == "" {
		return true
	}
	for _, w := range []string{"disc", "disk", "cd", "track", "game", "image"} {
		if t == w {
			return true
		}
	}
	// A title that is only digits and punctuation is no title.
	for _, r := range t {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') {
			return false
		}
	}
	return true
}

// GroupExplicit builds one logical title from discs the user named directly,
// as in:
//
//	ps2hdd install "Metal Gear Solid (Disc 1).cue" "Metal Gear Solid (Disc 2).cue"
//
// Unlike Group it does not infer boundaries: the caller has already said these
// discs belong together. Discs keep any number their filename carries and are
// numbered by position otherwise.
func GroupExplicit(discs []Disc, title string) model.Game {
	if len(discs) == 0 {
		return model.Game{Platform: model.PlatformPS1}
	}
	sort.SliceStable(discs, func(i, j int) bool {
		ni, nj := discs[i].DiscNumber, discs[j].DiscNumber
		if ni == nj {
			return false
		}
		if ni == 0 {
			return false
		}
		if nj == 0 {
			return true
		}
		return ni < nj
	})
	if title == "" {
		title = BaseTitle(filepath.Base(discs[0].SourcePath()))
	}
	if title == "" {
		title = discs[0].GameID
	}
	g := model.Game{
		Platform:   model.PlatformPS1,
		Title:      title,
		GameID:     discs[0].GameID,
		SourcePath: discs[0].SourcePath(),
	}
	for i, d := range discs {
		n := d.DiscNumber
		if n == 0 {
			n = i + 1
		}
		g.Discs = append(g.Discs, model.Disc{
			Number:     n,
			GameID:     d.GameID,
			Title:      d.Title,
			SourcePath: d.SourcePath(),
			SizeBytes:  d.SizeBytes,
		})
		g.SizeBytes += d.SizeBytes
	}
	return g
}
