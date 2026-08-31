package ps1

import (
	"strings"
	"testing"
)

// POPStarter finds its VCD by reading its own filename, so the launcher's name
// is not a naming choice ps2hdd gets to make. If this drifts, the game boots
// to a black screen with nothing to explain why.
func TestLauncherELFNameMatchesTheVCD(t *testing.T) {
	cases := map[string]string{
		"SLUS_002.40.Soul Blade.VCD":           "SLUS_002.40.Soul Blade.ELF",
		"SLUS_005.94.Metal Gear Solid_CD1.VCD": "SLUS_005.94.Metal Gear Solid_CD1.ELF",
		// A name carrying dots in the title keeps all of them: only the
		// extension is replaced.
		"SLUS_009.00.Vib.Ribbon.VCD": "SLUS_009.00.Vib.Ribbon.ELF",
	}
	for vcd, want := range cases {
		if got := LauncherELFName(vcd); got != want {
			t.Errorf("LauncherELFName(%q) = %q, want %q", vcd, got, want)
		}
		if got := LauncherDirName(vcd); got != strings.TrimSuffix(want, ELFExt) {
			t.Errorf("LauncherDirName(%q) = %q, want the shared base name", vcd, got)
		}
	}
}

// The directory carries the serial, so two releases of one title cannot land
// on top of each other.
func TestLauncherDirNamesAreDistinctPerRelease(t *testing.T) {
	a := LauncherDirName(VCDName("SLUS_005.94", "Metal Gear Solid", 1, 1))
	b := LauncherDirName(VCDName("SLES_012.34", "Metal Gear Solid", 1, 1))
	if a == b {
		t.Fatalf("two releases share the launcher directory %q", a)
	}
}

func TestLauncherTitleIsPrefixedAndBounded(t *testing.T) {
	if got := LauncherTitle("Soul Blade"); got != "[PS1] Soul Blade" {
		t.Errorf("got %q", got)
	}
	long := LauncherTitle(strings.Repeat("x", 400))
	if len(long) > maxAppTitleLen {
		t.Errorf("title is %d bytes, over OPL's %d", len(long), maxAppTitleLen)
	}
}

// OPL copies boot into a 64-byte field and launches "<path>/<boot>", so a
// longer name becomes a path that does not exist and the entry does nothing.
func TestBootNameFitsOPL(t *testing.T) {
	if !BootNameFitsOPL(strings.Repeat("a", maxBootNameLen)) {
		t.Error("a name of exactly the limit was rejected")
	}
	if BootNameFitsOPL(strings.Repeat("a", maxBootNameLen+1)) {
		t.Error("a name one over the limit was accepted")
	}
	// VCDName's own cap is POPStarter's 89, which is well past OPL's, so the
	// two limits really can disagree and the warning path is reachable.
	long := VCDName("SLUS_002.40", strings.Repeat("Long Title ", 12), 1, 1)
	if BootNameFitsOPL(LauncherELFName(long)) {
		t.Fatalf("expected %q to exceed OPL's boot limit", long)
	}
}

func TestTitleConfigRoundTrip(t *testing.T) {
	body := TitleConfigContents("[PS1] Soul Blade", "SLUS_002.40.Soul Blade.ELF")
	title, boot := ParseTitleConfig([]byte(body))
	if title != "[PS1] Soul Blade" || boot != "SLUS_002.40.Soul Blade.ELF" {
		t.Fatalf("round trip lost data: title=%q boot=%q", title, boot)
	}
	// OPL treats an indented line as part of a prefixed section and composes a
	// different key from it, so nothing we write may be indented.
	for _, line := range strings.Split(strings.TrimSuffix(body, "\n"), "\n") {
		if line != strings.TrimLeft(line, " \t") {
			t.Errorf("line is indented and OPL would key it differently: %q", line)
		}
	}
}

func TestParseTitleConfigMatchesOPL(t *testing.T) {
	cases := map[string]struct{ title, boot string }{
		"plain":            {"A", "B.ELF"},
		"crlf":             {"A", "B.ELF"},
		"comment":          {"A", "B.ELF"},
		"value with space": {"[PS1] Soul Blade", "SLUS_002.40.Soul Blade.ELF"},
		"equals in value":  {"A=B", "B.ELF"},
		"no boot":          {"A", ""},
		"no title":         {"", "B.ELF"},
		"indented boot":    {"A", ""},
		"junk":             {"", ""},
	}
	bodies := map[string]string{
		"plain":            "title=A\nboot=B.ELF\n",
		"crlf":             "title=A\r\nboot=B.ELF\r\n",
		"comment":          "# a note\ntitle=A\nboot=B.ELF\n",
		"value with space": "title=[PS1] Soul Blade\nboot=SLUS_002.40.Soul Blade.ELF\n",
		"equals in value":  "title=A=B\nboot=B.ELF\n",
		"no boot":          "title=A\n",
		"no title":         "boot=B.ELF\n",
		// OPL would file this under a composed key, not "boot", so reading it
		// as boot would make ps2hdd disagree with the console about whether
		// the launcher works.
		"indented boot": "title=A\n  boot=B.ELF\n",
		"junk":          "not a config at all\n",
	}
	for name, want := range cases {
		t.Run(name, func(t *testing.T) {
			title, boot := ParseTitleConfig([]byte(bodies[name]))
			if title != want.title || boot != want.boot {
				t.Errorf("got title=%q boot=%q, want title=%q boot=%q", title, boot, want.title, want.boot)
			}
		})
	}
}
