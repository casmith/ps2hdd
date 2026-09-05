package ps1

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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
	// Per-game files live in a support directory named after the disc's VCD,
	// under __common/POPS -- NOT beside the VCD in __.POPS. Both partitions
	// have a POPS-shaped directory in them and only one of them is read, which
	// is a mistake with no symptom until a disc change fails mid-game.
	//
	// Each disc of a multi-disc title gets its own support directory.
	//
	//	__.POPS/SLUS_005.94.Metal Gear Solid_CD1.VCD
	//	__common/POPS/SLUS_005.94.Metal Gear Solid_CD1/DISCS.TXT
	//	__common/POPS/SLUS_007.76.Metal Gear Solid_CD2/DISCS.TXT
	//	__common/POPS/SLUS_007.76.Metal Gear Solid_CD2/VMCDIR.TXT

	// DiscsFile lists every VCD of a multi-disc title, one per line, and goes
	// in every one of its discs' support directories. It is what POPStarter
	// offers the disc-swap menu from.
	DiscsFile = "DISCS.TXT"

	// VMCDirFile names the VCD whose virtual memory card the disc should use.
	// POPStarter otherwise gives each VCD its own card, so without this a save
	// made on disc 1 is invisible on disc 2. It goes in the support directory
	// of every disc after the first and holds disc 1's VCD filename.
	VMCDirFile = "VMCDIR.TXT"

	// CheatsFile holds raw cheat codes and POPStarter directives for one game.
	CheatsFile = "CHEATS.TXT"

	// Widescreen is the directive that turns on POPStarter's GTE widescreen
	// hack. It corrects 3D geometry and field of view; it does not correct
	// HUDs, fonts, menus or 2D backgrounds, and some games do not survive it,
	// which is why ps2hdd never applies it unasked.
	Widescreen = "$WIDESCREEN"

	// POPStarterELF is the launcher that ps2hdd copies and renames once per
	// installed title. Unlike the two files beside it, it is freely
	// distributable -- it is not Sony code -- but ps2hdd still does not carry
	// a copy, so it is taken from the runtime the user installed.
	POPStarterELF = "POPSTARTER.ELF"

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
	// SHA256 is the file's known content, lower-case hex, or "" when there is
	// no single right answer. POPS.ELF and the files beside it are one fixed
	// release that does not vary between POPStarter revisions, so they can be
	// checked exactly; POPSTARTER.ELF itself changes with every revision and
	// cannot be.
	SHA256 string
}

// RuntimeFiles is what a working POPStarter installation needs.
//
// The hashes are the published ones for the POPS release POPStarter is built
// against. They matter more than they look: a POPS.ELF that is the wrong file
// fails every PS1 title identically, whatever is done to the VCD, the launcher
// or its name, and there is nothing in the symptom to say so. Checking only
// that the files exist reports READY for a drive that cannot run anything.
var RuntimeFiles = []RuntimeFile{
	{
		Name:        "POPS.ELF",
		Description: "the POPS PlayStation emulator",
		Copyrighted: true,
		Required:    true,
		SHA256:      "59df3389c4df88a572daa720b05507c52c34eddfa0031a6fbeec55e0c2d0fcb1",
	},
	{
		Name:        "IOPRP252.IMG",
		Description: "the IOP replacement image POPS loads",
		Copyrighted: true,
		Required:    true,
		SHA256:      "3338b238d84d7d586b716677e3a1c03b2088b882ecfa17f91fc33798931ca3ba",
	},
	{
		Name:        POPStarterELF,
		Description: "the POPStarter launcher",
		Copyrighted: false,
		Required:    true,
	},
	// The two packages ship with POPS and belong beside it. They are not
	// marked required, because a setup missing them is not necessarily broken
	// and calling a working drive NOT READY would be worse than saying
	// nothing -- but they are imported and reported, which is what makes an
	// incomplete runtime visible.
	{
		Name:        "POPS.PAK",
		Description: "the POPS support package",
		Copyrighted: true,
		SHA256:      "a3973bc4d177f65dd3201afe508aa9b59dd8a4d3374369bff14fb01f920aacad",
	},
	{
		Name:        "POPS_IOX.PAK",
		Description: "the POPS I/O support package",
		Copyrighted: true,
		SHA256:      "9fa120429a73b632029b4f0fd554cd45c1e770f8ec020ecc3120b38a2b983e6e",
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
	// Wrong lists runtime files that are present but are not the file they
	// should be. A wrong POPS.ELF is indistinguishable from a right one until
	// a game is launched, and then every game fails the same way.
	Wrong []string `json:"wrong,omitempty"`
}

// Ready reports whether a PS1 game could be launched right now.
func (r Readiness) Ready() bool {
	return r.POPSPartition && r.CommonPartition && r.RuntimeChecked &&
		len(r.Missing) == 0 && len(r.Wrong) == 0
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
		out = append(out, fmt.Sprintf("The %s partition does not exist. Create it with `ps2hdd setup ps1 --create-pops <size>`, or with a PS2-side tool such as uLaunchELF.", POPSPartition))
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
	for _, name := range r.Wrong {
		f, _ := findRuntimeFile(name)
		out = append(out, fmt.Sprintf(
			"%s (%s) is present but is not the right file. Every PS1 title fails identically "+
				"with the wrong one, and nothing else in the setup will show why. Replace it and "+
				"re-import with `ps2hdd setup ps1 --import <dir>`; the expected SHA-256 is %s.",
			name, f.Description, f.SHA256))
	}
	for _, f := range RuntimeFiles {
		if f.Required || f.SHA256 == "" || r.Runtime[f.Name] {
			continue
		}
		out = append(out, fmt.Sprintf(
			"%s (%s) is not installed. It ships with POPS and belongs beside it; "+
				"PS1 support may work without it, but the runtime is incomplete.",
			f.Name, f.Description))
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
func CheckRuntime(commonMount string) (present map[string]bool, missing, wrong []string, err error) {
	dir := filepath.Join(commonMount, POPSDir)
	present = map[string]bool{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			// No POPS directory at all: everything required is missing, which
			// is a definite answer rather than an error.
			for _, f := range RuntimeFiles {
				present[f.Name] = false
				if f.Required {
					missing = append(missing, f.Name)
				}
			}
			return present, missing, nil, nil
		}
		return nil, nil, nil, fmt.Errorf("read %s: %w", dir, err)
	}
	// The name on disk may differ in case from the manifest's.
	have := map[string]string{}
	for _, e := range entries {
		if !e.IsDir() {
			have[strings.ToUpper(e.Name())] = e.Name()
		}
	}
	for _, f := range RuntimeFiles {
		actual, ok := have[strings.ToUpper(f.Name)]
		present[f.Name] = ok
		if !ok {
			if f.Required {
				missing = append(missing, f.Name)
			}
			continue
		}
		if f.SHA256 == "" {
			continue
		}
		sum, err := fileSHA256(filepath.Join(dir, actual))
		if err != nil {
			// Unreadable is not the same as wrong, and saying it is would send
			// a user replacing a file that may be fine.
			continue
		}
		if !strings.EqualFold(sum, f.SHA256) {
			wrong = append(wrong, f.Name)
		}
	}
	return present, missing, wrong, nil
}

// fileSHA256 hashes a runtime file. The whole runtime is a few megabytes, so
// this is cheap enough to do on every readiness check.
func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
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

// GameDirName is the per-title directory name: the VCD's base name.
//
// Two directories use it, in different partitions and for different reasons --
// +OPL/APPS/<name> holds the launcher OPL lists, __common/POPS/<name> holds the
// per-game files POPStarter reads -- and both are keyed off the VCD because
// that is the name POPStarter and OPL each derive their own lookups from.
func GameDirName(vcdName string) string {
	return strings.TrimSuffix(vcdName, filepath.Ext(vcdName))
}

// VMCDirContents renders a VMCDIR.TXT body: the VCD filename whose memory card
// this disc should share.
func VMCDirContents(firstDiscVCD string) string {
	return firstDiscVCD + "\n"
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
			Number:           n,
			GameID:           id,
			Title:            title,
			InstalledName:    e.Name(),
			SizeBytes:        size,
			InstallSizeBytes: size,
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
			Platform:         model.PlatformPS1,
			Title:            title,
			GameID:           b.discs[0].GameID,
			SizeBytes:        b.size,
			InstallSizeBytes: b.size,
			StorageBackend:   model.BackendPOPS,
			Installed:        true,
			Discs:            b.discs,
			PartitionName:    b.discs[0].InstalledName,
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
