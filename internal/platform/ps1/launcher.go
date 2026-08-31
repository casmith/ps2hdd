package ps1

import (
	"bufio"
	"bytes"
	"fmt"
	"path/filepath"
	"strings"
)

// Launchers: how a PS1 title becomes something you can select on the console.
//
// Open PS2 Loader has no PS1 support whatsoever. There is not one reference to
// POPS, POPSTARTER or .VCD anywhere in its source, and hddsupport.c knows only
// about HDLoader partitions. So a VCD sitting in __.POPS is data and nothing
// more: it appears in no menu, on any OPL build.
//
// What appears in a menu is a copy of POPSTARTER.ELF renamed after the VCD.
// POPStarter reads its own filename to decide which VCD to mount, and OPL
// finds it through its Apps page, which works like this (src/opl.c:505,550):
//
//   - oplScanApps lists "<prefix>APPS" for every enabled device; on HDD the
//     prefix is the partition named by hdd_partition in conf_hdd.cfg, which
//     OPL itself writes as +OPL (src/hddsupport.c:148).
//   - Only subdirectories are considered. An ELF loose in APPS is skipped.
//   - Each subdirectory must hold a title.cfg giving both title and boot.
//     appScanCallback drops the entry when either is absent, with nothing on
//     screen to say so (src/appsupport.c:202).
//
// So one title needs a directory, a renamed ELF and a title.cfg, and the whole
// thing is silent when it is wrong. That silence is why ps2hdd writes all
// three itself and why doctor checks for them afterwards.
const (
	// AppsDir is the directory inside +OPL that OPL scans for applications.
	AppsDir = "APPS"

	// TitleConfigFile is the per-application config OPL reads. The name is
	// exact: APP_TITLE_CONFIG_FILE in include/appsupport.h.
	TitleConfigFile = "title.cfg"

	// TitleKey and BootKey are the two keys an entry must have.
	TitleKey = "title"
	BootKey  = "boot"

	// LauncherTitlePrefix marks PS1 entries in an apps list that also holds
	// homebrew. It is the community convention and it makes them sort together.
	LauncherTitlePrefix = "[PS1] "

	// maxBootNameLen is APP_BOOT_MAX from include/appsupport.h. OPL copies the
	// boot value into a fixed 64-byte field and terminates it there
	// (src/appsupport.c:222), then launches "<path>/<boot>"
	// (src/appsupport.c:447). A longer name is therefore truncated into a path
	// that does not exist, and the entry appears in the list but does nothing.
	maxBootNameLen = 64

	// maxAppTitleLen is APP_TITLE_MAX, the same treatment applied to the
	// displayed name. Overrunning it is cosmetic rather than fatal.
	maxAppTitleLen = 128
)

// LauncherELFName returns the launcher filename for an installed VCD.
//
// POPStarter locates its VCD by its own filename, so the two must match apart
// from the extension. This is the one part of the layout that is not a choice.
func LauncherELFName(vcdName string) string {
	return strings.TrimSuffix(vcdName, filepath.Ext(vcdName)) + ELFExt
}

// LauncherDirName returns the +OPL/APPS subdirectory holding a title's
// launcher.
//
// It is the VCD's base name, which carries the serial and is therefore already
// unique -- two releases of one title cannot collide -- and which makes the
// correspondence with __.POPS visible to anyone browsing the disk.
func LauncherDirName(vcdName string) string {
	return strings.TrimSuffix(vcdName, filepath.Ext(vcdName))
}

// LauncherTitle renders the name OPL displays in its apps list.
func LauncherTitle(title string) string {
	t := LauncherTitlePrefix + strings.TrimSpace(title)
	if len(t) > maxAppTitleLen {
		t = strings.TrimRight(t[:maxAppTitleLen], " ")
	}
	return t
}

// BootNameFitsOPL reports whether OPL can launch a boot filename of this
// length. See maxBootNameLen: a longer one is silently truncated.
func BootNameFitsOPL(bootName string) bool {
	return len(bootName) <= maxBootNameLen
}

// TitleConfigContents renders a title.cfg body.
//
// Lines are written without leading whitespace on purpose. OPL treats an
// indented line as a continuation of a prefixed section and composes its key
// as "<prefix>_<key>" (src/config.c:485), so an indented "boot=" is not read
// as boot at all.
func TitleConfigContents(displayTitle, bootName string) string {
	return fmt.Sprintf("%s=%s\n%s=%s\n", TitleKey, displayTitle, BootKey, bootName)
}

// ParseTitleConfig reads the title and boot keys out of a title.cfg.
//
// It mirrors OPL's parser closely enough to agree with it about what counts as
// an entry: '#' starts a comment, CRLF is tolerated, the value runs to the end
// of the line, and a line that has no '=' is ignored. Indented lines are
// skipped rather than misread, because OPL would key them differently and
// would not find them under "boot" either.
func ParseTitleConfig(body []byte) (title, boot string) {
	sc := bufio.NewScanner(bytes.NewReader(body))
	sc.Buffer(make([]byte, 0, 8192), 1<<16)
	for sc.Scan() {
		line := strings.TrimSuffix(sc.Text(), "\r")
		if line == "" || line[0] == '#' {
			continue
		}
		if line[0] == ' ' || line[0] == '\t' {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(k)) {
		case TitleKey:
			title = v
		case BootKey:
			boot = v
		}
	}
	return title, boot
}
