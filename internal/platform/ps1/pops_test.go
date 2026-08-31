package ps1_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

func TestVCDName(t *testing.T) {
	cases := []struct {
		id, title   string
		disc, total int
		want        string
	}{
		{"SLUS_000.67", "Castlevania - Symphony of the Night", 1, 1,
			"SLUS_000.67.Castlevania - Symphony of the Night.VCD"},
		{"SLUS_005.94", "Metal Gear Solid", 1, 2, "SLUS_005.94.Metal Gear Solid_CD1.VCD"},
		{"SLUS_007.76", "Metal Gear Solid", 2, 2, "SLUS_007.76.Metal Gear Solid_CD2.VCD"},
		// A dashed serial is normalised to the OPL form OPL and POPStarter use.
		{"SLUS-00067", "Game", 1, 1, "SLUS_000.67.Game.VCD"},
	}
	for _, c := range cases {
		if got := ps1.VCDName(c.id, c.title, c.disc, c.total); got != c.want {
			t.Errorf("VCDName(%q,%q,%d,%d) = %q\nwant %q", c.id, c.title, c.disc, c.total, got, c.want)
		}
	}
}

func TestVCDNameRespectsLengthLimit(t *testing.T) {
	long := strings.Repeat("Very Long Title ", 20)
	for _, total := range []int{1, 3} {
		name := ps1.VCDName("SLUS_005.94", long, 2, total)
		if len(name) > 89 {
			t.Errorf("total=%d: name is %d characters, over POPStarter's 89 limit: %q", total, len(name), name)
		}
		if !strings.HasSuffix(name, ".VCD") {
			t.Errorf("total=%d: truncation lost the extension: %q", total, name)
		}
		if total > 1 && !strings.Contains(name, "_CD2") {
			t.Errorf("total=%d: truncation lost the disc suffix: %q", total, name)
		}
	}
}

func TestSanitizeName(t *testing.T) {
	cases := map[string]string{
		"Ape Escape":                       "Ape Escape",
		"Tomb Raider: The Last Revelation": "Tomb Raider The Last Revelation",
		`Weird\/Name`:                      "Weird Name",
		"  padded  ":                       "padded",
		"trailing.":                        "trailing",
	}
	for in, want := range cases {
		if got := ps1.SanitizeName(in); got != want {
			t.Errorf("SanitizeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseVCDName(t *testing.T) {
	cases := []struct {
		name      string
		id, title string
		disc      int
		multi     bool
	}{
		{"SLUS_000.67.Castlevania - Symphony of the Night.VCD", "SLUS_000.67", "Castlevania - Symphony of the Night", 1, false},
		{"SLUS_005.94.Metal Gear Solid_CD1.VCD", "SLUS_005.94", "Metal Gear Solid", 1, true},
		{"SLUS_007.76.Metal Gear Solid_CD2.VCD", "SLUS_007.76", "Metal Gear Solid", 2, true},
	}
	for _, c := range cases {
		id, title, disc, multi := ps1.ParseVCDName(c.name)
		if id != c.id || title != c.title || disc != c.disc || multi != c.multi {
			t.Errorf("ParseVCDName(%q) = %q,%q,%d,%v\nwant %q,%q,%d,%v",
				c.name, id, title, disc, multi, c.id, c.title, c.disc, c.multi)
		}
	}
}

func TestVCDNameRoundTrip(t *testing.T) {
	name := ps1.VCDName("SLUS_005.94", "Metal Gear Solid", 2, 2)
	id, title, disc, multi := ps1.ParseVCDName(name)
	if id != "SLUS_005.94" || title != "Metal Gear Solid" || disc != 2 || !multi {
		t.Errorf("round trip of %q gave %q,%q,%d,%v", name, id, title, disc, multi)
	}
}

func TestScanPOPSGroupsMultiDisc(t *testing.T) {
	dir := t.TempDir()
	files := []string{
		"SLUS_000.67.Castlevania - Symphony of the Night.VCD",
		"SLUS_005.94.Metal Gear Solid_CD1.VCD",
		"SLUS_007.76.Metal Gear Solid_CD2.VCD",
		"notes.txt", // must be ignored
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(dir, f), make([]byte, 1024), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "SLUS_005.94.Metal Gear Solid"), 0o755); err != nil {
		t.Fatal(err)
	}

	games, err := ps1.ScanPOPS(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(games) != 2 {
		t.Fatalf("got %d games, want 2: %+v", len(games), games)
	}
	byTitle := map[string]int{}
	for _, g := range games {
		byTitle[g.Title] = g.DiscCount()
		if !g.Installed || g.StorageBackend != "pops" {
			t.Errorf("%s: Installed=%v backend=%q", g.Title, g.Installed, g.StorageBackend)
		}
	}
	if byTitle["Metal Gear Solid"] != 2 {
		t.Errorf("Metal Gear Solid grouped as %d discs", byTitle["Metal Gear Solid"])
	}
	if byTitle["Castlevania - Symphony of the Night"] != 1 {
		t.Errorf("single-disc title = %d discs", byTitle["Castlevania - Symphony of the Night"])
	}
}

func TestCheckRuntimeMissing(t *testing.T) {
	dir := t.TempDir() // no POPS directory at all
	present, missing, err := ps1.CheckRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 3 {
		t.Errorf("missing = %v, want all three runtime files", missing)
	}
	for name, ok := range present {
		if ok {
			t.Errorf("%s reported present in an empty partition", name)
		}
	}
}

func TestCheckRuntimeComplete(t *testing.T) {
	dir := t.TempDir()
	pops := filepath.Join(dir, "POPS")
	if err := os.Mkdir(pops, 0o755); err != nil {
		t.Fatal(err)
	}
	// PFS filenames are conventionally upper case but users copy in whatever
	// case they have, so matching must ignore it.
	for _, f := range []string{"POPS.ELF", "ioprp252.img", "PopStarter.elf"} {
		if err := os.WriteFile(filepath.Join(pops, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	present, missing, err := ps1.CheckRuntime(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(missing) != 0 {
		t.Errorf("missing = %v, want none", missing)
	}
	if !present["IOPRP252.IMG"] {
		t.Error("case-insensitive match failed")
	}

	r := ps1.Readiness{POPSPartition: true, CommonPartition: true, Runtime: present, RuntimeChecked: true}
	if !r.Ready() || r.Status() != "READY" {
		t.Errorf("Readiness = %v %q", r.Ready(), r.Status())
	}
	if len(r.Explain()) != 0 {
		t.Errorf("a ready installation explained itself: %v", r.Explain())
	}
}

func TestReadinessExplainsCopyrightedFiles(t *testing.T) {
	r := ps1.Readiness{
		POPSPartition:   true,
		CommonPartition: true,
		RuntimeChecked:  true,
		Missing:         []string{"POPS.ELF", "IOPRP252.IMG"},
	}
	if r.Ready() || r.Status() != "NOT READY" {
		t.Fatalf("Readiness = %v", r.Ready())
	}
	lines := strings.Join(r.Explain(), "\n")
	if !strings.Contains(lines, "cannot legally distribute") {
		t.Errorf("explanation should say why ps2hdd does not supply POPS.ELF:\n%s", lines)
	}
	if !strings.Contains(lines, "setup ps1 --import") {
		t.Errorf("explanation should be actionable:\n%s", lines)
	}
}

func TestReadinessUnknownRuntimeIsNotAbsent(t *testing.T) {
	// If __common could not be mounted, the runtime status is unknown; saying
	// "missing" would send the user chasing a file that may well be there.
	r := ps1.Readiness{POPSPartition: true, CommonPartition: true, RuntimeChecked: false}
	if r.Ready() {
		t.Error("unknown runtime status reported as ready")
	}
	lines := strings.Join(r.Explain(), "\n")
	if !strings.Contains(lines, "unknown") {
		t.Errorf("explanation should say the status is unknown:\n%s", lines)
	}
}

func TestDiscsFile(t *testing.T) {
	names := []string{
		ps1.VCDName("SLUS_005.94", "Metal Gear Solid", 1, 2),
		ps1.VCDName("SLUS_007.76", "Metal Gear Solid", 2, 2),
	}
	got := ps1.DiscsFileContents(names)
	want := names[0] + "\n" + names[1] + "\n"
	if got != want {
		t.Errorf("DISCS.TXT =\n%q\nwant\n%q", got, want)
	}
	// Each disc gets its own support directory, named after its own VCD --
	// including the _CD suffix, because the directories are keyed on the file
	// POPStarter is looking at and not on the release.
	if dir := ps1.GameDirName(names[0]); dir != "SLUS_005.94.Metal Gear Solid_CD1" {
		t.Errorf("GameDirName(disc 1) = %q", dir)
	}
	if dir := ps1.GameDirName(names[1]); dir != "SLUS_007.76.Metal Gear Solid_CD2" {
		t.Errorf("GameDirName(disc 2) = %q", dir)
	}
}

// VMCDIR.TXT names the VCD whose memory card the disc shares, one line. A
// documented way to get this wrong is to put the title in it instead.
func TestVMCDirContents(t *testing.T) {
	first := ps1.VCDName("SLUS_005.94", "Metal Gear Solid", 1, 2)
	if got, want := ps1.VMCDirContents(first), first+"\n"; got != want {
		t.Errorf("VMCDIR.TXT = %q, want %q", got, want)
	}
}
