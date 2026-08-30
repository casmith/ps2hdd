package app

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/external"
)

func writeOSRelease(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "os-release")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestDetectDistroFamily(t *testing.T) {
	cases := map[string]struct {
		body       string
		wantID     string
		wantFamily string
	}{
		"arch by id": {
			body:   "ID=arch\nPRETTY_NAME=\"Arch Linux\"\n",
			wantID: "arch", wantFamily: "arch",
		},
		// A derivative is recognised through ID_LIKE without being listed.
		"manjaro by id": {
			body:   "ID=manjaro\nID_LIKE=arch\nPRETTY_NAME=\"Manjaro Linux\"\n",
			wantID: "manjaro", wantFamily: "arch",
		},
		"mint through id_like": {
			body:   "ID=linuxmint\nID_LIKE=\"ubuntu debian\"\nPRETTY_NAME=\"Linux Mint 22\"\n",
			wantID: "linuxmint", wantFamily: "debian",
		},
		"unlisted derivative through id_like": {
			body:   "ID=garuda\nID_LIKE=arch\n",
			wantID: "garuda", wantFamily: "arch",
		},
		"fedora": {
			body:   "ID=fedora\nPRETTY_NAME=\"Fedora Linux 41\"\n",
			wantID: "fedora", wantFamily: "fedora",
		},
		"rocky through id_like": {
			body:   "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n",
			wantID: "rocky", wantFamily: "fedora",
		},
		"alpine": {
			body:   "ID=alpine\nPRETTY_NAME=\"Alpine Linux v3.20\"\n",
			wantID: "alpine", wantFamily: "alpine",
		},
		// Something nobody has heard of gets no family, and therefore no
		// invented package manager.
		"unknown": {
			body:   "ID=plan9\nPRETTY_NAME=\"Plan 9\"\n",
			wantID: "plan9", wantFamily: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			d := distroFrom(writeOSRelease(t, tc.body))
			if d.ID != tc.wantID || d.Family != tc.wantFamily {
				t.Errorf("got id=%q family=%q, want id=%q family=%q", d.ID, d.Family, tc.wantID, tc.wantFamily)
			}
			if tc.wantFamily != "" && !d.Known() {
				t.Error("Known() is false for a recognised family")
			}
		})
	}
}

// A missing os-release must not panic or invent a distribution.
func TestDetectDistroWithoutOSRelease(t *testing.T) {
	d := distroFrom(filepath.Join(t.TempDir(), "absent"))
	if d.Known() || d.ID != "" {
		t.Errorf("got %+v, want an empty unknown distro", d)
	}
}

func TestRemedyForPackagedTools(t *testing.T) {
	cases := map[string]struct{ family, tool, want string }{
		"arch 7z":         {"arch", external.SevenZipTool, "sudo pacman -S --needed 7zip"},
		"debian 7z":       {"debian", external.SevenZipTool, "sudo apt install p7zip-full"},
		"fedora 7z":       {"fedora", external.SevenZipTool, "sudo dnf install p7zip"},
		"suse lsblk":      {"suse", "lsblk", "sudo zypper install util-linux"},
		"alpine fusermnt": {"alpine", external.FusermountTool, "sudo apk add fuse3"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			r := Remedy(tc.tool, Distro{Family: tc.family, ID: tc.family})
			if len(r.Commands) != 1 || r.Commands[0] != tc.want {
				t.Errorf("commands = %v, want [%q]", r.Commands, tc.want)
			}
			if r.FromSource {
				t.Error("a packaged tool was marked as built from source")
			}
		})
	}
}

// hdl_dump, pfsfuse and pfsshell are in no distribution's repositories. The
// remedy has to say so and give the build, or a reader goes looking for a
// package that was never there.
func TestRemedyForUnpackagedTools(t *testing.T) {
	for _, tool := range []string{external.HDLDumpTool, external.PFSFuseTool, external.PFSShellTool} {
		t.Run(tool, func(t *testing.T) {
			r := Remedy(tool, Distro{ID: "arch", Family: "arch"})
			if !r.FromSource {
				t.Error("FromSource is false")
			}
			if !strings.Contains(r.Note, "not packaged") {
				t.Errorf("note does not say it is unpackaged: %q", r.Note)
			}
			if len(r.Commands) == 0 {
				t.Fatal("no build commands given")
			}
			joined := strings.Join(r.Commands, "\n")
			if !strings.Contains(joined, "git clone") {
				t.Errorf("no clone step:\n%s", joined)
			}
			// /usr/local/bin is the one location sudo's secure_path includes.
			if !strings.Contains(joined, "/usr/local/bin") && !strings.Contains(joined, "meson install") {
				t.Errorf("build does not install anywhere sudo can see it:\n%s", joined)
			}
		})
	}

	// The two traps that cost real time are named where they are needed.
	r := Remedy(external.PFSFuseTool, Distro{ID: "debian", Family: "debian"})
	if !strings.Contains(r.Note, "FUSE 2") {
		t.Errorf("note does not warn about FUSE 2: %q", r.Note)
	}
	if !strings.Contains(strings.Join(r.Commands, "\n"), "-Denable_pfsfuse=true") {
		t.Errorf("build omits the flag that produces pfsfuse at all: %v", r.Commands)
	}
	if !strings.Contains(strings.Join(r.Commands, "\n"), "libfuse-dev") {
		t.Errorf("debian build deps do not include FUSE 2 headers: %v", r.Commands)
	}
}

// An unrecognised distribution gets no commands at all. A wrong sudo line is
// worse than none, and this is the case where one would be invented.
func TestRemedyOnUnknownDistroInventsNothing(t *testing.T) {
	unknown := Distro{ID: "plan9"}
	for _, tool := range []string{external.SevenZipTool, external.HDLDumpTool} {
		r := Remedy(tool, unknown)
		for _, c := range r.Commands {
			if strings.Contains(c, "pacman") || strings.Contains(c, "apt") ||
				strings.Contains(c, "dnf") || strings.Contains(c, "zypper") || strings.Contains(c, "apk") {
				t.Errorf("%s: invented a package manager on an unknown distro: %q", tool, c)
			}
		}
		if !strings.Contains(r.Note, "docs/dependencies.md") {
			t.Errorf("%s: note does not point anywhere: %q", tool, r.Note)
		}
	}
}
