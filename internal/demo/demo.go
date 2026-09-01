// Package demo builds a self-contained, writable PS2 HDD environment so that
// ps2hdd can be driven end to end without a console, a HDD, or any of the
// external tools installed.
//
// It exists because every interesting path in this program touches a raw block
// device, and a program whose interesting paths can only be exercised on
// hardware is a program nobody can review. `ps2hdd --demo` builds a synthetic
// APA disk image, synthetic source directories, and a command runner that
// stands in for hdl_dump and pfsfuse, then runs the real CLI and TUI against
// them. Nothing here is reachable unless the user passes --demo.
package demo

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/apa/apasynth"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/iso9660"
	"github.com/casmith/ps2hdd/internal/iso9660/isosynth"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// Env is a demo environment rooted at a directory.
type Env struct {
	Root string
	// Image is the synthetic APA disk image, used as the configured device.
	Image string

	// real runs the commands worth running for real -- 7z, which only reads
	// the source library and writes into scratch.
	real external.Runner

	mu   sync.Mutex
	disk apasynth.Disk
	// partitions maps an APA partition id to the directory that stands in for
	// its PFS contents.
	partitions map[string]string
}

// Setup builds a demo environment under root, creating it if needed.
func Setup(root string) (*Env, error) {
	if root == "" {
		base, err := config.CacheDir()
		if err != nil {
			return nil, err
		}
		root = filepath.Join(base, "demo")
	}
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, err
	}
	e := &Env{
		Root:       root,
		Image:      filepath.Join(root, "ps2hdd-demo.img"),
		disk:       apasynth.DefaultDisk(),
		partitions: map[string]string{},
		real:       &external.ExecRunner{},
	}
	// A demo that resets on every command would be misleading: installing a
	// game and then listing the library must show what the first command did.
	// The synthetic disk's contents are therefore persisted alongside it.
	fresh, err := e.loadState()
	if err != nil {
		return nil, err
	}
	if err := e.writeImage(); err != nil {
		return nil, err
	}
	if err := e.buildPartitions(fresh); err != nil {
		return nil, err
	}
	if err := e.buildSources(fresh); err != nil {
		return nil, err
	}
	return e, nil
}

// statePath is where the synthetic disk's game list is kept between runs.
func (e *Env) statePath() string { return filepath.Join(e.Root, "state.json") }

// loadState restores a previous run's disk contents. It reports whether the
// environment is fresh, in which case the pre-populated partitions are laid
// down; on a restored environment they are left as the user left them.
func (e *Env) loadState() (fresh bool, err error) {
	data, err := os.ReadFile(e.statePath())
	if err != nil {
		if os.IsNotExist(err) {
			return true, nil
		}
		return false, err
	}
	var d apasynth.Disk
	if err := json.Unmarshal(data, &d); err != nil {
		// A corrupt state file resets the demo rather than failing it.
		return true, nil
	}
	e.disk = d
	return false, nil
}

func (e *Env) saveState() error {
	data, err := json.Marshal(e.disk)
	if err != nil {
		return err
	}
	return os.WriteFile(e.statePath(), data, 0o600)
}

// Config returns a configuration pointed at the demo environment.
func (e *Env) Config(base config.Config) config.Config {
	cfg := base
	cfg.Device = e.Image
	cfg.Sources.PS2 = e.PS2Source()
	cfg.Sources.PS1 = e.PS1Source()
	// The demo has no network, so artwork comes from the bundled mirror.
	cfg.Assets.Provider = "local"
	cfg.Assets.Mirror = filepath.Join(e.Root, "art-mirror")
	return cfg
}

// PS2Source and PS1Source are the synthetic source directories.
func (e *Env) PS2Source() string { return filepath.Join(e.Root, "sources", "ps2") }
func (e *Env) PS1Source() string { return filepath.Join(e.Root, "sources", "psx") }

// Runner returns a command runner that stands in for the external tools.
func (e *Env) Runner() external.Runner {
	f := external.NewFakeRunner()
	f.Handler = e.handle
	return f
}

// Cleanup removes the environment.
func (e *Env) Cleanup() error { return os.RemoveAll(e.Root) }

func (e *Env) writeImage() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err := apasynth.Write(e.Image, e.disk); err != nil {
		return err
	}
	return e.saveState()
}

// buildPartitions creates a backing directory per PFS partition, pre-populated
// the way a lived-in HDD would be.
func (e *Env) buildPartitions(fresh bool) error {
	for _, p := range e.disk.Parts {
		dir := filepath.Join(e.Root, "partitions", partitionDir(p.ID))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		e.partitions[strings.ToLower(p.ID)] = dir
	}
	if !fresh {
		// The partition trees are ordinary directories that survive between
		// runs, so a restored environment needs no seeding.
		return nil
	}

	opl := e.partitions[strings.ToLower("+OPL")]
	for _, sub := range []string{"ART", "CFG", "CHT", "THM", "VMC"} {
		if err := os.MkdirAll(filepath.Join(opl, sub), 0o755); err != nil {
			return err
		}
	}
	// One game already has a cover, so the Assets view shows both states.
	if err := os.WriteFile(filepath.Join(opl, "ART", "SLUS_210.50_COV.png"), pngPixel(), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(opl, "CFG", "SLUS_210.50.cfg"), []byte("$ConfigSource=1\nTitle=Burnout 3 Takedown\n"), 0o644); err != nil {
		return err
	}

	// A PS1 title already installed, so the unified library is not all PS2.
	pops := e.partitions[strings.ToLower(ps1.POPSPartition)]
	vcd := ps1.VCDName("SLUS_000.67", "Castlevania - Symphony of the Night", 1, 1)
	if err := os.WriteFile(filepath.Join(pops, vcd), make([]byte, ps1.HeaderSize+4096), 0o644); err != nil {
		return err
	}

	// __common holds only POPSTARTER.ELF: the two Sony files are deliberately
	// absent, so the demo shows the "NOT READY" path a real user hits before
	// supplying their own copies.
	common := e.partitions[strings.ToLower(ps1.CommonPartition)]
	popsDir := filepath.Join(common, ps1.POPSDir)
	if err := os.MkdirAll(popsDir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(popsDir, "POPSTARTER.ELF"), []byte("demo launcher"), 0o644); err != nil {
		return err
	}

	// A small artwork mirror so `art sync` has something to install.
	mirror := filepath.Join(e.Root, "art-mirror")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		return err
	}
	for _, id := range []string{"SLUS_215.03", "SLUS_200.02", "SLUS_000.67", "SCUS_941.63"} {
		for _, t := range []model.AssetType{model.AssetCover, model.AssetBackground} {
			name := fmt.Sprintf("%s_%s.png", id, t)
			if err := os.WriteFile(filepath.Join(mirror, name), pngPixel(), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// buildSources writes synthetic installable images: a couple of PS2 ISOs, a
// single-disc PS1 title and a three-disc one.
func (e *Env) buildSources(fresh bool) error {
	if !fresh {
		// Rewriting the source images on every run would change their
		// modification times, which is exactly what invalidates the scan
		// cache -- so a restored environment leaves them alone and the demo
		// shows the caching a real library gets.
		return nil
	}
	ps2Dir := e.PS2Source()
	if err := os.MkdirAll(ps2Dir, 0o755); err != nil {
		return err
	}
	ps2Games := []struct {
		file, serial string
		padMB        int
		// cd marks a CD-XA title. Ridge Racer V really was a PS2 CD, so the
		// demo carries one of each and the media-type path is exercised.
		cd bool
	}{
		{"Gran Turismo 4.iso", "SCUS_973.28", 900, false},
		{"Shadow of the Colossus.iso", "SCUS_974.72", 800, false},
		{"Ridge Racer V.iso", "SLUS_200.02", 8, true},
		{"Burnout 3 Takedown.iso", "SLUS_210.50", 850, false},
	}
	for _, g := range ps2Games {
		pad := uint32(g.padMB) * 1024 * 1024 / iso9660.LogicalSectorSize
		data, err := isosynth.Build(isosynth.Image{
			VolumeID:  g.serial,
			PadBlocks: pad,
			CDXA:      g.cd,
			Files:     map[string][]byte{"SYSTEM.CNF": isosynth.PS2SystemCNF(g.serial)},
		})
		if err != nil {
			return err
		}
		if err := writeSparse(filepath.Join(ps2Dir, g.file), data); err != nil {
			return err
		}
	}

	ps1Dir := e.PS1Source()
	if err := os.MkdirAll(ps1Dir, 0o755); err != nil {
		return err
	}
	if err := writePS1(filepath.Join(ps1Dir, "Castlevania - Symphony of the Night"), "Castlevania - Symphony of the Night", "SLUS_000.67"); err != nil {
		return err
	}
	ff7 := filepath.Join(ps1Dir, "Final Fantasy VII")
	for i, serial := range []string{"SCUS_941.63", "SCUS_941.64", "SCUS_941.65"} {
		if err := writePS1(ff7, fmt.Sprintf("Disc %d", i+1), serial); err != nil {
			return err
		}
	}
	mgs := filepath.Join(ps1Dir, "Metal Gear Solid")
	for i, serial := range []string{"SLUS_005.94", "SLUS_007.76"} {
		if err := writePS1(mgs, fmt.Sprintf("Disc %d", i+1), serial); err != nil {
			return err
		}
	}
	return nil
}

// writePS1 writes a MODE2/2352 BIN and its cuesheet.
func writePS1(dir, base, serial string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	data, err := isosynth.BuildMode2352(isosynth.Image{
		VolumeID: serial,
		Files:    map[string][]byte{"SYSTEM.CNF": isosynth.PS1SystemCNF(serial)},
	})
	if err != nil {
		return err
	}
	binName := base + ".bin"
	if err := os.WriteFile(filepath.Join(dir, binName), data, 0o644); err != nil {
		return err
	}
	sheet := fmt.Sprintf("FILE %q BINARY\n  TRACK 01 MODE2/2352\n    INDEX 01 00:00:00\n", binName)
	return os.WriteFile(filepath.Join(dir, base+".cue"), []byte(sheet), 0o644)
}

// writeSparse writes the leading content of a file and extends it with a hole,
// so a nominally 900 MB demo ISO costs a few kilobytes on disk.
func writeSparse(path string, data []byte) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	// The ISO structures live in the first blocks; the padding past them is
	// zeroes either way, so only the head needs writing.
	head := data
	if len(head) > 1<<20 {
		head = head[:1<<20]
	}
	if _, err := f.Write(head); err != nil {
		return err
	}
	if err := f.Truncate(int64(len(data))); err != nil {
		return err
	}
	return f.Close()
}

// handle stands in for the external tools.
// failSubstring names a title the fake tools should refuse to act on.
//
// It exists so the demo can exercise failures it cannot produce on its own:
// the fake hdl_dump never reads the source image, so corrupting one changes
// nothing, and the fake pfsshell always succeeds. What needs testing is not
// the failure but what happens around it -- that a batch carries on past a bad
// title, and that a pfsshell which exits 0 having done nothing is caught by
// reading the partition table back -- and neither can be tested without one.
const failEnv = "PS2HDD_DEMO_FAIL"

func (e *Env) handle(c external.Command) (external.Result, error) {
	ctx := context.Background()
	if want := os.Getenv(failEnv); want != "" && c.Name == external.HDLDumpTool {
		for _, a := range c.Args {
			if strings.Contains(a, want) {
				return external.Result{Stderr: "demo: injected failure"},
					fmt.Errorf("hdl_dump: injected failure for %q", want)
			}
		}
	}
	switch c.Name {
	case external.PFSFuseTool:
		return external.Result{}, e.mount(c.Args)
	case external.FusermountTool, external.FusermountLegcy:
		return external.Result{}, e.unmount(c.Args)
	case external.HDLDumpTool:
		return e.hdlDump(c)
	case external.PFSShellTool:
		return e.pfsShell(c)
	case external.SevenZipTool, external.SevenZipAltTool:
		// Archives are the one thing worth doing for real. 7z only reads the
		// source library and writes into scratch -- it never touches the
		// synthetic drive -- so faking it would remove the coverage without
		// removing any risk, and the archive paths would go untested.
		return e.real.Run(ctx, c)
	case "lsblk":
		// The demo device is a file, so no block device enumeration applies.
		return external.Result{Stdout: `{"blockdevices":[]}`}, nil
	default:
		return external.Result{}, nil
	}
}

// mount replaces the mountpoint with a symlink to the partition's backing
// directory. A real mount is impossible without privileges, and a symlink is
// indistinguishable to every caller in this program, all of which just read
// and write ordinary files under the mountpoint.
func (e *Env) mount(args []string) error {
	var partition, mountpoint string
	for _, a := range args {
		if rest, ok := strings.CutPrefix(a, "--partition="); ok {
			partition = rest
		}
	}
	if len(args) > 0 {
		mountpoint = args[len(args)-1]
	}
	e.mu.Lock()
	dir, ok := e.partitions[strings.ToLower(partition)]
	e.mu.Unlock()
	if !ok {
		return fmt.Errorf("pfsfuse could not find partition %s", partition)
	}
	if err := os.Remove(mountpoint); err != nil && !os.IsNotExist(err) {
		return err
	}
	return os.Symlink(dir, mountpoint)
}

func (e *Env) unmount(args []string) error {
	for _, a := range args {
		if a == "-u" {
			continue
		}
		if fi, err := os.Lstat(a); err == nil && fi.Mode()&os.ModeSymlink != 0 {
			return os.Remove(a)
		}
	}
	return nil
}

// hdlDump simulates installing and removing games by rewriting the synthetic
// image, which is exactly what hdl_dump does to a real one.
func (e *Env) hdlDump(c external.Command) (external.Result, error) {
	if len(c.Args) == 0 {
		return external.Result{}, fmt.Errorf("hdl_dump: no command")
	}
	switch c.Args[0] {
	case "inject_cd", "inject_dvd":
		// inject_* target name source [startup] ...
		if len(c.Args) < 5 {
			return external.Result{}, fmt.Errorf("hdl_dump: malformed inject command")
		}
		name, source, startup := c.Args[2], c.Args[3], c.Args[4]
		fi, err := os.Stat(source)
		if err != nil {
			return external.Result{}, err
		}
		sizeMB := uint32(fi.Size() / (1024 * 1024))
		if sizeMB == 0 {
			sizeMB = 1
		}
		// Report progress so the queue and the CLI progress parser are
		// exercised the same way they would be for real.
		if c.OnStdout != nil {
			for _, pct := range []int{0, 25, 50, 75, 100} {
				c.OnStdout(fmt.Sprintf("[====>    ] %3d%%, 00:00:10 remaining, 20.00 MB/sec", pct))
			}
		}
		e.mu.Lock()
		e.disk.Games = append(e.disk.Games, apasynth.Game{
			Name: name, Startup: startup, SizeMB: sizeMB, IsDVD: c.Args[0] == "inject_dvd",
		})
		e.mu.Unlock()
		return external.Result{}, e.writeImage()

	}
	return external.Result{}, nil
}

// pfsShell stands in for pfsshell, which is driven through stdin rather than
// argv.
//
// It exists because removing a game goes through `rmpart`. hdl_dump has no verb
// for that -- upstream compiled its "delete" out -- and the demo used to fake
// the missing verb, so every removal passed here and failed on real hardware.
// A fake that answers a command the real tool rejects is worse than no fake:
// it turns the one place that would have caught this into evidence that
// nothing was wrong.
//
// Like the real thing it exits 0 regardless; callers confirm by re-reading the
// partition table.
func (e *Env) pfsShell(c external.Command) (external.Result, error) {
	if c.Stdin == nil {
		return external.Result{}, nil
	}
	script, err := io.ReadAll(c.Stdin)
	if err != nil {
		return external.Result{}, err
	}
	// An injected failure models pfsshell's real one: it prints the error and
	// still exits 0, so nothing but re-reading the partition table can tell
	// that the command did not work. That check is the point of the hook.
	if want := os.Getenv(failEnv); want != "" && strings.Contains(string(script), want) {
		return external.Result{Stdout: "(!) demo: injected failure.\n"}, nil
	}
	var out strings.Builder
	for _, line := range strings.Split(string(script), "\n") {
		fields := splitPFSLine(line)
		if len(fields) == 0 {
			continue
		}
		switch fields[0] {
		case "rmpart":
			if len(fields) < 2 {
				out.WriteString("(!) Exit code is -1.\n")
				continue
			}
			if !e.removePartition(fields[1]) {
				out.WriteString("(!) Exit code is -1.\n")
			}
		case "mkpart":
			if len(fields) < 3 {
				out.WriteString("(!) Exit code is -1.\n")
				continue
			}
			if !e.addPartition(fields[1], fields[2]) {
				out.WriteString("(!) Exit code is -1.\n")
			}
		}
	}
	return external.Result{Stdout: out.String()}, e.writeImage()
}

// splitPFSLine splits a pfsshell command line, honouring the quotes a name
// with spaces in it needs.
func splitPFSLine(line string) []string {
	var out []string
	var cur strings.Builder
	inQuote := false
	for _, r := range strings.TrimSpace(line) {
		switch {
		case r == '"':
			inQuote = !inQuote
		case (r == ' ' || r == '\t') && !inQuote:
			if cur.Len() > 0 {
				out = append(out, cur.String())
				cur.Reset()
			}
		default:
			cur.WriteRune(r)
		}
	}
	if cur.Len() > 0 {
		out = append(out, cur.String())
	}
	return out
}

// removePartition empties a game or a PFS partition in the synthetic table.
//
// It leaves an "__empty" partition of the same size behind, because that is
// what APA does: apaRemovePartition rewrites the header in place rather than
// unlinking it, and the entry stays in the chain meaning "this space is free".
// Deleting the entry outright -- which this used to do -- models a disk that
// no real removal produces, and hides whether the space is ever given back.
func (e *Env) removePartition(name string) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	games := e.disk.Games[:0]
	found := false
	for _, g := range e.disk.Games {
		if apasynth.SanitizePartitionName(g.Startup, g.Name) == name {
			found = true
			e.disk.Parts = append(e.disk.Parts, apasynth.PFSPart{
				ID: apa.EmptyPartitionID, SizeMB: g.SizeMB,
			})
			continue
		}
		games = append(games, g)
	}
	e.disk.Games = append([]apasynth.Game(nil), games...)
	if found {
		return true
	}
	for i, p := range e.disk.Parts {
		if strings.EqualFold(p.ID, name) {
			e.disk.Parts[i].ID = apa.EmptyPartitionID
			return true
		}
	}
	return false
}

// addPartition creates a PFS partition, for `mkpart`. The size is pfsshell's
// own form: a whole number followed by M or G.
func (e *Env) addPartition(name, size string) bool {
	mb, err := strconv.Atoi(strings.TrimRight(size, "MmGg"))
	if err != nil || mb <= 0 {
		return false
	}
	if strings.HasSuffix(strings.ToUpper(size), "G") {
		mb *= 1024
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	for _, p := range e.disk.Parts {
		if strings.EqualFold(p.ID, name) {
			return false
		}
	}
	e.disk.Parts = append(e.disk.Parts, apasynth.PFSPart{ID: name, SizeMB: uint32(mb)})
	return true
}

func partitionDir(id string) string {
	s := strings.NewReplacer("+", "plus-", ".", "-", "/", "-").Replace(id)
	return strings.Trim(strings.ToLower(s), "-_")
}

// pngPixel is a valid 1x1 PNG, so demo artwork is a real image file rather
// than arbitrary bytes.
func pngPixel() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0d, 0x0a, 0x1a, 0x0a,
		0x00, 0x00, 0x00, 0x0d, 'I', 'H', 'D', 'R',
		0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01,
		0x08, 0x06, 0x00, 0x00, 0x00, 0x1f, 0x15, 0xc4, 0x89,
		0x00, 0x00, 0x00, 0x0a, 'I', 'D', 'A', 'T',
		0x78, 0x9c, 0x63, 0x00, 0x01, 0x00, 0x00, 0x05, 0x00, 0x01,
		0x0d, 0x0a, 0x2d, 0xb4,
		0x00, 0x00, 0x00, 0x00, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
}
