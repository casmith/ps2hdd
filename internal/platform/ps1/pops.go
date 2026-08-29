package ps1

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// POPStarter layout on an APA HDD.
//
// A PS1 title is a .VCD file in the __.POPS partition, named "<serial>.<title>"
// so that OPL and POPStarter can key artwork and per-game data off the serial.
// The POPS emulator itself and its IOP replacement image are Sony code that
// ships with no redistribution rights, so ps2hdd detects them, reports whether
// they are present, and can import copies the user supplies -- but never
// carries them.
//
// References: the POPStarter documentation and the ps2-home HDD guides.
const (
	// POPSPartition is the PFS partition holding the VCDs.
	POPSPartition = "__.POPS"
	// CommonPartition holds the POPS runtime, shared by every title.
	CommonPartition = "__common"
	// POPSDir is the directory inside __common holding the runtime.
	POPSDir = "POPS"

	// VCDExt is the extension of an installed PS1 disc image.
	VCDExt = ".VCD"
	// ELFExt is the extension of a per-title POPStarter launcher.
	ELFExt = ".ELF"
	// DiscsFile lists the VCDs of a multi-disc title for POPStarter's disc
	// swap feature. It lives in a directory named after the title.
	DiscsFile = "DISCS.TXT"

	// maxVCDNameLen is POPStarter's filename limit.
	maxVCDNameLen = 89
)

// RuntimeFile is a component of the POPS runtime.
type RuntimeFile struct {
	// Name is the filename inside __common/POPS.
	Name string
	// Description explains what the file is.
	Description string
	// Copyrighted marks a Sony file that ps2hdd will never distribute.
	Copyrighted bool
	// Required marks a file without which PS1 playback cannot work.
	Required bool
}

// RuntimeFiles is what a working POPStarter installation needs.
var RuntimeFiles = []RuntimeFile{
	{
		Name:        "POPS.ELF",
		Description: "the POPS PlayStation emulator",
		Copyrighted: true,
		Required:    true,
	},
	{
		Name:        "IOPRP252.IMG",
		Description: "the IOP replacement image POPS loads",
		Copyrighted: true,
		Required:    true,
	},
	{
		Name:        "POPSTARTER.ELF",
		Description: "the POPStarter launcher",
		Copyrighted: false,
		Required:    true,
	},
}

// Readiness reports whether PS1 support is usable.
type Readiness struct {
	POPSPartition   bool `json:"pops_partition"`
	CommonPartition bool `json:"common_partition"`
	// Runtime maps each runtime filename to whether it is present. It is only
	// populated when __common could actually be inspected.
	Runtime map[string]bool `json:"runtime"`
	// RuntimeChecked is false when __common could not be mounted, in which
	// case a missing entry means "unknown", not "absent".
	RuntimeChecked bool `json:"runtime_checked"`
	// Missing lists the required runtime files that were not found.
	Missing []string `json:"missing,omitempty"`
}

// Ready reports whether a PS1 game could be launched right now.
func (r Readiness) Ready() bool {
	return r.POPSPartition && r.CommonPartition && r.RuntimeChecked && len(r.Missing) == 0
}

// Status renders READY or NOT READY.
func (r Readiness) Status() string {
	if r.Ready() {
		return "READY"
	}
	return "NOT READY"
}

// Explain returns human-readable, actionable lines describing what is missing.
func (r Readiness) Explain() []string {
	var out []string
	if !r.POPSPartition {
		out = append(out, fmt.Sprintf("The %s partition does not exist. Create it with a PS2-side tool such as uLaunchELF, or with pfsshell.", POPSPartition))
	}
	if !r.CommonPartition {
		out = append(out, fmt.Sprintf("The %s partition does not exist; the POPS runtime has nowhere to live.", CommonPartition))
	}
	if !r.RuntimeChecked {
		if r.CommonPartition {
			out = append(out, fmt.Sprintf("%s could not be inspected, so the runtime status is unknown. Check that pfsfuse is installed.", CommonPartition))
		}
		return out
	}
	for _, name := range r.Missing {
		f, ok := findRuntimeFile(name)
		if !ok {
			out = append(out, fmt.Sprintf("%s is missing from %s/%s.", name, CommonPartition, POPSDir))
			continue
		}
		if f.Copyrighted {
			out = append(out, fmt.Sprintf(
				"%s (%s) is missing. It is Sony code that ps2hdd cannot legally distribute; "+
					"supply your own copy and import it with `ps2hdd setup ps1 --import <dir>`.",
				name, f.Description))
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s (%s) is missing. Obtain it from the POPStarter release and import it with `ps2hdd setup ps1 --import <dir>`.",
			name, f.Description))
	}
	return out
}

func findRuntimeFile(name string) (RuntimeFile, bool) {
	for _, f := range RuntimeFiles {
		if strings.EqualFold(f.Name, name) {
			return f, true
		}
	}
	return RuntimeFile{}, false
}

// CheckRuntime inspects a mounted __common partition and fills in the runtime
// half of a Readiness.
func CheckRuntime(commonMount string) (map[string]bool, []string, error) {
	dir := filepath.Join(commonMount, POPSDir)
	present := map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No POPS directory at all: everything required is missing, which
			// is a definite answer rather than an error.
			var missing []string
			for _, f := range RuntimeFiles {
				present[f.Name] = false
				if f.Required {
					missing = append(missing, f.Name)
				}
			}
			return present, missing, nil
		}
		return nil, nil, fmt.Errorf("read %s: %w", dir, err)
	}
	have := map[string]bool{}
	for _, e := range entries {
		if !e.IsDir() {
			have[strings.ToUpper(e.Name())] = true
		}
	}
	var missing []string
	for _, f := range RuntimeFiles {
		ok := have[strings.ToUpper(f.Name)]
		present[f.Name] = ok
		if !ok && f.Required {
			missing = append(missing, f.Name)
		}
	}
	return present, missing, nil
}

// VCDName builds the filename an installed disc gets inside __.POPS.
//
// The convention is "<serial>.<title>.VCD" for a single-disc title and
// "<serial>.<title>_CD<n>.VCD" for each disc of a multi-disc one, which is
// what POPStarter's disc-swap feature and OPL's artwork lookup both expect.
// Characters PFS cannot hold are replaced, and the name is truncated to
// POPStarter's 89-character limit with the disc suffix preserved.
func VCDName(gameID, title string, disc, totalDiscs int) string {
	id := model.OPLGameID(gameID)
	suffix := ""
	if totalDiscs > 1 {
		suffix = fmt.Sprintf("_CD%d", disc)
	}
	clean := SanitizeName(title)
	if clean == "" {
		clean = id
	}
	base := id + "." + clean
	room := maxVCDNameLen - len(suffix) - len(VCDExt)
	if len(base) > room {
		base = strings.TrimRight(base[:room], " .")
	}
	return base + suffix + VCDExt
}

// DiscsDirName is the directory beside the VCDs that holds DISCS.TXT for a
// multi-disc title. POPStarter looks for it under the first disc's base name.
func DiscsDirName(gameID, title string) string {
	return strings.TrimSuffix(VCDName(gameID, title, 1, 2), "_CD1"+VCDExt)
}

// DiscsFileContents renders the DISCS.TXT body listing a title's VCDs in disc
// order, one filename per line.
func DiscsFileContents(vcdNames []string) string {
	var b strings.Builder
	for _, n := range vcdNames {
		b.WriteString(n)
		b.WriteString("\n")
	}
	return b.String()
}

// unsafeNameChars are characters that either PFS rejects or that would make a
// filename ambiguous to POPStarter.
const unsafeNameChars = `/\:*?"<>|` + "\x00"

// SanitizeName makes a title safe to use as part of a PFS filename.
func SanitizeName(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r < 0x20 || strings.ContainsRune(unsafeNameChars, r) {
			b.WriteRune(' ')
			continue
		}
		b.WriteRune(r)
	}
	return strings.Trim(strings.Join(strings.Fields(b.String()), " "), " .")
}

// InstalledGame is one PS1 title discovered in a mounted __.POPS partition.
type InstalledGame struct {
	Title  string
	GameID string
	Discs  []model.Disc
	Size   int64
}

// ScanPOPS lists the PS1 titles installed in a mounted __.POPS partition.
//
// Multi-disc titles are recognised by the "_CD<n>" suffix and folded back into
// one logical title, so that the library shows what the user thinks of as one
// game rather than three.
func ScanPOPS(mount string) ([]model.Game, error) {
	entries, err := os.ReadDir(mount)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", mount, err)
	}
	type bucket struct {
		title  string
		gameID string
		discs  []model.Disc
		size   int64
	}
	buckets := map[string]*bucket{}
	var order []string

	for _, e := range entries {
		if e.IsDir() || !strings.EqualFold(filepath.Ext(e.Name()), VCDExt) {
			continue
		}
		info, err := e.Info()
		var size int64
		if err == nil {
			size = info.Size()
		}
		id, title, disc, multi := ParseVCDName(e.Name())
		// The discs of one release carry different serials, so a multi-disc
		// title is grouped by its title alone; the serial of disc 1 becomes
		// the title's identity below. Single-disc entries are keyed on their
		// whole filename so two unrelated games can never be merged.
		key := "s|" + strings.ToLower(e.Name())
		if multi {
			key = "m|" + strings.ToLower(title)
		}
		b, ok := buckets[key]
		if !ok {
			b = &bucket{title: title, gameID: id}
			buckets[key] = b
			order = append(order, key)
		}
		n := disc
		if !multi {
			n = 1
		}
		b.discs = append(b.discs, model.Disc{
			Number:        n,
			GameID:        id,
			Title:         title,
			InstalledName: e.Name(),
			SizeBytes:     size,
		})
		b.size += size
	}

	out := make([]model.Game, 0, len(order))
	for _, k := range order {
		b := buckets[k]
		sort.SliceStable(b.discs, func(i, j int) bool { return b.discs[i].Number < b.discs[j].Number })
		title := b.title
		if title == "" {
			title = b.gameID
		}
		out = append(out, model.Game{
			Platform:       model.PlatformPS1,
			Title:          title,
			GameID:         b.discs[0].GameID,
			SizeBytes:      b.size,
			StorageBackend: model.BackendPOPS,
			Installed:      true,
			Discs:          b.discs,
			PartitionName:  b.discs[0].InstalledName,
		})
	}
	return out, nil
}

// vcdDiscSuffix matches the "_CD2" a multi-disc VCD carries.
var vcdDiscSuffix = func() func(string) (string, int, bool) {
	return func(base string) (string, int, bool) {
		i := strings.LastIndex(strings.ToUpper(base), "_CD")
		if i < 0 {
			return base, 0, false
		}
		digits := base[i+3:]
		if digits == "" || len(digits) > 2 {
			return base, 0, false
		}
		n := 0
		for _, c := range digits {
			if c < '0' || c > '9' {
				return base, 0, false
			}
			n = n*10 + int(c-'0')
		}
		if n == 0 {
			return base, 0, false
		}
		return base[:i], n, true
	}
}()

// ParseVCDName splits an installed VCD filename back into its parts.
func ParseVCDName(name string) (gameID, title string, disc int, multi bool) {
	base := strings.TrimSuffix(name, filepath.Ext(name))
	base, disc, multi = vcdDiscSuffix(base)
	// The serial is the leading "SLUS_005.94." prefix.
	if id := model.FindGameID(base); id != "" {
		gameID = id
		if i := strings.Index(strings.ToUpper(base), strings.ToUpper(strings.ReplaceAll(id, " ", ""))); i >= 0 {
			rest := base[i+len(id):]
			title = strings.TrimLeft(rest, " .-_")
		}
	}
	if title == "" {
		title = strings.TrimLeft(base, " .-_")
	}
	if !multi {
		disc = 1
	}
	return gameID, title, disc, multi
}
