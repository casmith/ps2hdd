package app

import (
	"bufio"
	"os"
	"strings"

	"github.com/casmith/ps2hdd/internal/external"
)

// Distro identifies the host distribution well enough to name a package
// manager. Anything unrecognised is reported as such rather than guessed at:
// a wrong `sudo` command is worse than none.
type Distro struct {
	// ID is the os-release ID, e.g. "manjaro".
	ID string `json:"id,omitempty"`
	// Family is the package-manager family this belongs to: arch, debian,
	// fedora, suse or alpine. Empty when unknown.
	Family string `json:"family,omitempty"`
	// Name is the human-readable PRETTY_NAME, for the report.
	Name string `json:"name,omitempty"`
}

// Known reports whether the family is one this package can advise on.
func (d Distro) Known() bool { return d.Family != "" }

// families maps os-release IDs to a package-manager family. ID_LIKE is
// consulted too, which is what makes derivatives work without listing every
// one: Manjaro says arch, Mint says ubuntu debian, Rocky says rhel fedora.
var families = map[string]string{
	"arch": "arch", "manjaro": "arch", "endeavouros": "arch", "cachyos": "arch",
	"debian": "debian", "ubuntu": "debian", "linuxmint": "debian", "pop": "debian", "raspbian": "debian",
	"fedora": "fedora", "rhel": "fedora", "centos": "fedora", "rocky": "fedora", "almalinux": "fedora", "nobara": "fedora",
	"opensuse": "suse", "opensuse-tumbleweed": "suse", "opensuse-leap": "suse", "sles": "suse",
	"alpine": "alpine",
}

// DetectDistro reads /etc/os-release.
func DetectDistro() Distro {
	return distroFrom("/etc/os-release")
}

func distroFrom(path string) Distro {
	f, err := os.Open(path)
	if err != nil {
		return Distro{}
	}
	defer f.Close()

	fields := map[string]string{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		key, value, ok := strings.Cut(sc.Text(), "=")
		if !ok {
			continue
		}
		fields[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"'`)
	}

	d := Distro{ID: fields["ID"], Name: fields["PRETTY_NAME"]}
	if d.Name == "" {
		d.Name = d.ID
	}
	if fam, ok := families[d.ID]; ok {
		d.Family = fam
		return d
	}
	// ID_LIKE is space-separated and ordered most-specific first.
	for _, like := range strings.Fields(fields["ID_LIKE"]) {
		if fam, ok := families[like]; ok {
			d.Family = fam
			return d
		}
	}
	return d
}

// packages maps a tool to the package providing it, per family.
//
// Only tools that are actually packaged appear here. hdl_dump, pfsfuse and
// pfsshell are absent from every distribution's repositories and from
// Homebrew, so they are handled separately: there is no package name to print.
var packages = map[string]map[string]string{
	"lsblk": {
		"arch": "util-linux", "debian": "util-linux", "fedora": "util-linux",
		"suse": "util-linux", "alpine": "util-linux",
	},
	external.SevenZipTool: {
		"arch": "7zip", "debian": "p7zip-full", "fedora": "p7zip",
		"suse": "7zip", "alpine": "7zip",
	},
	external.FusermountTool: {
		"arch": "fuse3", "debian": "fuse3", "fedora": "fuse3",
		"suse": "fuse3", "alpine": "fuse3",
	},
}

// installCommand renders the family's install verb for a package.
func installCommand(family, pkg string) string {
	switch family {
	case "arch":
		return "sudo pacman -S --needed " + pkg
	case "debian":
		return "sudo apt install " + pkg
	case "fedora":
		return "sudo dnf install " + pkg
	case "suse":
		return "sudo zypper install " + pkg
	case "alpine":
		return "sudo apk add " + pkg
	}
	return ""
}

// buildDeps is what the source builds need, per family. These are the same
// package lists docs/dependencies.md carries; they live here so `doctor` can
// print them at the moment they are needed rather than sending the user to
// find a file.
var buildDeps = map[string]string{
	"arch":   "base-devel git meson ninja fuse2 fuse3",
	"debian": "build-essential git meson ninja-build libfuse-dev fuse3",
	"fedora": "@development-tools git meson ninja-build fuse-devel fuse3",
	"suse":   "patterns-devel-base-devel_basis git meson ninja fuse-devel fuse3",
	"alpine": "build-base git meson ninja fuse-dev fuse3",
}

// ToolRemedy is how to obtain one missing tool on this system.
type ToolRemedy struct {
	Tool string `json:"tool"`
	// Commands are shell lines to run, in order. Empty when the distribution
	// is unknown, in which case Note carries the explanation.
	Commands []string `json:"commands,omitempty"`
	// Note is prose for the cases a command cannot express.
	Note string `json:"note,omitempty"`
	// FromSource marks a tool no distribution packages.
	FromSource bool `json:"from_source,omitempty"`
}

// Remedy returns how to install a tool on the given distribution.
//
// A tool nothing packages gets the build-dependency command plus a pointer to
// the build steps, because printing a package name that does not exist would
// send someone looking for a package that was never there.
func Remedy(tool string, d Distro) ToolRemedy {
	r := ToolRemedy{Tool: tool}

	if byFamily, ok := packages[tool]; ok {
		pkg, known := byFamily[d.Family]
		if !known || !d.Known() {
			r.Note = "install the package providing " + tool + "; see docs/dependencies.md"
			return r
		}
		r.Commands = []string{installCommand(d.Family, pkg)}
		return r
	}

	// hdl_dump, pfsfuse and pfsshell.
	r.FromSource = true
	switch tool {
	case external.HDLDumpTool:
		r.Note = "not packaged by any distribution; built from source"
		if deps, ok := buildDeps[d.Family]; ok && d.Known() {
			r.Commands = []string{
				installCommand(d.Family, deps),
				"git clone --recursive https://github.com/ps2homebrew/hdl-dump.git",
				"cd hdl-dump && make RELEASE=yes",
				"sudo install -m0755 hdl_dump /usr/local/bin/",
			}
		}
	case external.PFSFuseTool, external.PFSShellTool:
		r.Note = "not packaged by any distribution; built from source. " +
			"pfsfuse is off by default in the build and wants FUSE 2, not FUSE 3"
		if deps, ok := buildDeps[d.Family]; ok && d.Known() {
			r.Commands = []string{
				installCommand(d.Family, deps),
				"git clone --recursive https://github.com/ps2homebrew/pfsshell.git",
				"cd pfsshell && PKG_CONFIG_PATH=/usr/lib/pkgconfig meson setup build -Denable_pfsfuse=true",
				"PKG_CONFIG_PATH=/usr/lib/pkgconfig meson compile -C build",
				"sudo meson install -C build",
			}
		}
	}
	if len(r.Commands) == 0 && r.Note != "" {
		r.Note += "; see docs/dependencies.md"
	}
	return r
}

// InstallHint is the advice about where installed tools must live, which is
// not obvious on a system where ps2hdd is run under sudo.
//
// It applies to every tool at once, so it is returned alongside the remedies
// rather than repeated on each one.
func InstallHint() string {
	const base = "Install to /usr/local/bin: sudo's secure_path does not include ~/.local/bin, " +
		"so a tool installed there is invisible when ps2hdd runs under sudo."
	if !homebrewPresent() {
		return base
	}
	return base + " Homebrew is installed here and is not the answer either: it packages none of " +
		"the PS2 tools, secure_path excludes it too, and its util-linux is built without libudev " +
		"and shadows the system lsblk."
}

// homebrewPresent reports whether Homebrew is installed.
//
// The check is by directory rather than by looking `brew` up on PATH, because
// this runs under sudo as often as not and secure_path excludes Homebrew --
// which is the very thing being warned about.
func homebrewPresent() bool {
	dirs := []string{"/home/linuxbrew/.linuxbrew/bin", "/opt/homebrew/bin", "/usr/local/Homebrew"}
	if prefix := os.Getenv("HOMEBREW_PREFIX"); prefix != "" {
		dirs = append(dirs, prefix+"/bin")
	}
	for _, d := range dirs {
		if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			return true
		}
	}
	return false
}
