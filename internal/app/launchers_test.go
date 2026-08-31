package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// writeLauncherDir builds one +OPL/APPS entry.
func writeLauncherDir(t *testing.T, oplMount, dir, body string) {
	t.Helper()
	d := filepath.Join(oplMount, ps1.AppsDir, dir)
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if body == "" {
		return
	}
	if err := os.WriteFile(filepath.Join(d, ps1.TitleConfigFile), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// scanLaunchers has to agree with OPL about what counts as an entry, or the
// audit reports a problem the console does not have, or misses one it does.
func TestScanLaunchersAgreesWithOPL(t *testing.T) {
	mp := t.TempDir()
	writeLauncherDir(t, mp, "good", ps1.TitleConfigContents("[PS1] Good", "GOOD.ELF"))
	// OPL drops an entry missing either key, so neither is a launcher.
	writeLauncherDir(t, mp, "no-boot", "title=Only A Title\n")
	writeLauncherDir(t, mp, "no-title", "boot=NOTITLE.ELF\n")
	// A directory with no title.cfg at all is skipped.
	writeLauncherDir(t, mp, "bare", "")
	// Case is not significant: the console's filesystem is not case sensitive
	// about these names and neither is the check.
	writeLauncherDir(t, mp, "cased", ps1.TitleConfigContents("[PS1] Cased", "cased.elf"))

	// OPL considers only subdirectories (d_type != DT_DIR is skipped), so a
	// loose file is not an entry.
	if err := os.WriteFile(filepath.Join(mp, ps1.AppsDir, "loose.elf"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	// Nor is a symlink to one. This is the case that separates "checks the
	// directory type" from "happens to fail reading title.cfg out of a file":
	// following the link would find a perfectly good config that OPL, reading
	// the directory entry's own type, would never look at.
	outside := filepath.Join(t.TempDir(), "elsewhere")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, ps1.TitleConfigFile),
		[]byte(ps1.TitleConfigContents("[PS1] Linked", "LINKED.ELF")), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(mp, ps1.AppsDir, "linked")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	boots := map[string]bool{}
	if err := scanLaunchers(mp, boots); err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{"GOOD.ELF": true, "CASED.ELF": true}
	if len(boots) != len(want) {
		t.Fatalf("scanned %v, want %v", boots, want)
	}
	for k := range want {
		if !boots[k] {
			t.Errorf("%s was not found", k)
		}
	}
}

// A drive with no APPS directory has no launchers, which is an answer rather
// than a failure to look. Returning an error here would make doctor report
// "unknown" on every fresh +OPL partition.
func TestScanLaunchersWithoutAppsDirectory(t *testing.T) {
	boots := map[string]bool{}
	if err := scanLaunchers(t.TempDir(), boots); err != nil {
		t.Fatalf("got %v, want no error", err)
	}
	if len(boots) != 0 {
		t.Errorf("found %v on a drive with no APPS directory", boots)
	}
}

// The launcher points at disc 1, because POPStarter swaps to the rest itself.
// Pointing at another disc would start a multi-disc game in the middle.
func TestLauncherVCDPicksDiscOne(t *testing.T) {
	g := model.Game{Discs: []model.Disc{
		{Number: 2, InstalledName: "B_CD2.VCD"},
		{Number: 1, InstalledName: "A_CD1.VCD"},
		{Number: 3, InstalledName: "C_CD3.VCD"},
	}}
	if got := launcherVCD(g); got != "A_CD1.VCD" {
		t.Errorf("got %q, want the disc 1 VCD", got)
	}
	if got := launcherVCD(model.Game{}); got != "" {
		t.Errorf("got %q for a game with no discs, want empty", got)
	}
	// A single-disc title recorded without a disc number still resolves.
	one := model.Game{Discs: []model.Disc{{InstalledName: "Solo.VCD"}}}
	if got := launcherVCD(one); got != "Solo.VCD" {
		t.Errorf("got %q, want Solo.VCD", got)
	}
}

func TestLauncherAuditExplainsAndReportsOK(t *testing.T) {
	if !(LauncherAudit{Checked: true}).OK() {
		t.Error("a checked, empty audit is not OK")
	}
	// Unchecked must never read as OK: an empty Missing then means "unknown",
	// and calling that a pass is how a silent failure stays silent.
	if (LauncherAudit{}).OK() {
		t.Error("an unchecked audit reported OK")
	}
	a := LauncherAudit{Checked: true, Installed: 3, Missing: []string{"A"}, TooLong: []string{"B"}}
	if a.OK() {
		t.Error("an audit with findings reported OK")
	}
	lines := a.Explain()
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want one per finding: %v", len(lines), lines)
	}
	// Each line has to name the titles and a way forward, or doctor is just
	// telling the user something is wrong.
	if !strings.Contains(lines[0], "A") || !strings.Contains(lines[0], "--launchers") {
		t.Errorf("the missing-launcher line does not name the title and the fix: %q", lines[0])
	}
	if !strings.Contains(lines[1], "B") {
		t.Errorf("the too-long line does not name the title: %q", lines[1])
	}
}
