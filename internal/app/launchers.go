package app

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/casmith/ps2hdd/internal/drive"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// LauncherAudit reports which installed PS1 titles the console can actually
// list.
//
// The failure it exists to catch is entirely silent. A VCD with no launcher is
// a complete, correct, verified install that simply never appears in a menu,
// and OPL says nothing about it because OPL does not know PS1 games exist. The
// only way to notice is to look for the launcher, which is what this does.
type LauncherAudit struct {
	// Checked is false when +OPL could not be inspected, in which case an
	// empty Missing means "unknown", not "all present".
	Checked bool `json:"checked"`
	// Installed counts the PS1 titles considered.
	Installed int `json:"installed"`
	// Missing names the titles with no launcher on the HDD.
	Missing []string `json:"missing,omitempty"`
	// TooLong names titles whose launcher exists but whose filename exceeds
	// what OPL can boot. They appear on the Apps page and do nothing.
	TooLong []string `json:"too_long,omitempty"`
}

// OK reports whether every installed PS1 title is launchable.
func (a LauncherAudit) OK() bool {
	return a.Checked && len(a.Missing) == 0 && len(a.TooLong) == 0
}

// Explain returns actionable lines describing what is wrong.
func (a LauncherAudit) Explain() []string {
	var out []string
	if len(a.Missing) > 0 {
		out = append(out, fmt.Sprintf(
			"%d installed PS1 title(s) have no POPStarter launcher in %s/%s, so OPL will not "+
				"list them however long you look: %s. Write the missing launchers with "+
				"`ps2hdd setup ps1 --launchers`.",
			len(a.Missing), drive.PartitionOPL, ps1.AppsDir, strings.Join(a.Missing, ", ")))
	}
	if len(a.TooLong) > 0 {
		out = append(out, fmt.Sprintf(
			"%d installed PS1 title(s) have a launcher filename longer than the 64 characters "+
				"OPL can boot, so their Apps entries do nothing: %s. Launch them from "+
				"wLaunchELF, or reinstall them under a shorter title.",
			len(a.TooLong), strings.Join(a.TooLong, ", ")))
	}
	return out
}

// AuditPS1Launchers checks every installed PS1 title for a working launcher.
//
// It looks for the launcher the way OPL does rather than for the filenames
// ps2hdd would have written: every title.cfg under +OPL/APPS is read, and a
// title counts as launchable if any of them boots its VCD's ELF. A launcher
// somebody made by hand in a differently named directory therefore passes,
// which is the point -- the question is whether the console can start the
// game, not whether ps2hdd was the one to arrange it.
//
// Only the HDD is examined. OPL also scans the memory cards, so a launcher on
// mc0 would work and would be reported here as missing; that is why the
// wording says "on the HDD".
func (s *Services) AuditPS1Launchers(ctx context.Context) (LauncherAudit, error) {
	var a LauncherAudit
	games, err := s.InstalledPS1(ctx)
	if err != nil {
		return a, err
	}
	a.Installed = len(games)
	if len(games) == 0 {
		a.Checked = true
		return a, nil
	}
	m, err := s.Mounts(ctx)
	if err != nil {
		return a, err
	}
	boots := map[string]bool{}
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		return scanLaunchers(mp, boots)
	})
	if err != nil {
		return a, err
	}
	a.Checked = true
	for _, g := range games {
		vcd := launcherVCD(g)
		if vcd == "" {
			continue
		}
		elf := ps1.LauncherELFName(vcd)
		switch {
		case !boots[strings.ToUpper(elf)]:
			a.Missing = append(a.Missing, g.Title)
		case !ps1.BootNameFitsOPL(elf):
			a.TooLong = append(a.TooLong, g.Title)
		}
	}
	return a, nil
}

// scanLaunchers fills boots with the upper-cased boot filename of every apps
// entry OPL would accept.
func scanLaunchers(oplMount string, boots map[string]bool) error {
	root := filepath.Join(oplMount, ps1.AppsDir)
	entries, err := os.ReadDir(root)
	if err != nil {
		// No APPS directory at all is a definite answer -- nothing is
		// registered -- not a failure to look.
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", root, err)
	}
	for _, e := range entries {
		// OPL considers only subdirectories (src/opl.c:505).
		if !e.IsDir() {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, e.Name(), ps1.TitleConfigFile))
		if err != nil {
			continue
		}
		title, boot := ps1.ParseTitleConfig(body)
		// OPL drops an entry that lacks either key (src/appsupport.c:202), so
		// one that lacks either is not a launcher for this purpose.
		if title == "" || boot == "" {
			continue
		}
		boots[strings.ToUpper(boot)] = true
	}
	return nil
}

// launcherVCD returns the VCD a title's launcher must point at: disc 1, since
// POPStarter swaps to the rest itself through DISCS.TXT.
func launcherVCD(g model.Game) string {
	for _, d := range g.Discs {
		if d.Number <= 1 && d.InstalledName != "" {
			return d.InstalledName
		}
	}
	if len(g.Discs) > 0 {
		return g.Discs[0].InstalledName
	}
	return ""
}

// RepairPS1Launchers writes a launcher for every installed PS1 title that has
// none, and reports the titles it fixed.
//
// It is additive: a title that already has a launcher is left alone, including
// one made by hand, so running it is safe at any time.
func (s *Services) RepairPS1Launchers(ctx context.Context) ([]string, error) {
	a, err := s.AuditPS1Launchers(ctx)
	if err != nil {
		return nil, err
	}
	if len(a.Missing) == 0 {
		return nil, nil
	}
	games, err := s.InstalledPS1(ctx)
	if err != nil {
		return nil, err
	}
	want := map[string]bool{}
	for _, t := range a.Missing {
		want[t] = true
	}
	if s.DryRun {
		return a.Missing, nil
	}

	ready, err := s.PS1Readiness(ctx)
	if err != nil {
		return nil, err
	}
	if ready.RuntimeChecked && !ready.Runtime[ps1.POPStarterELF] {
		return nil, fmt.Errorf("%w: %s is not in %s/%s; import it with `ps2hdd setup ps1 --import <dir>`",
			ErrNotReady, ps1.POPStarterELF, ps1.CommonPartition, ps1.POPSDir)
	}

	unlock := s.LockHDD()
	defer unlock()

	m, err := s.Mounts(ctx)
	if err != nil {
		return nil, err
	}
	staging, err := os.MkdirTemp("", "ps2hdd-launcher-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(staging)

	// One copy of POPSTARTER.ELF serves every launcher, so __common is mounted
	// once rather than once per title.
	local, err := fetchPOPStarter(ctx, m, staging)
	if err != nil {
		return nil, err
	}

	var fixed []string
	err = m.With(ctx, drive.PartitionOPL, func(mp string) error {
		for _, g := range games {
			if err := ctx.Err(); err != nil {
				return err
			}
			if !want[g.Title] {
				continue
			}
			vcd := launcherVCD(g)
			if vcd == "" {
				continue
			}
			if err := writeLauncher(ctx, mp, local, vcd, g.Title); err != nil {
				return fmt.Errorf("%s: %w", g.Title, err)
			}
			fixed = append(fixed, g.Title)
		}
		return nil
	})
	if err != nil {
		return fixed, err
	}
	logging.ContextLogger(ctx).Info("repaired POPStarter launchers", "count", len(fixed))
	return fixed, nil
}
