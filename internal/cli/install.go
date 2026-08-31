package cli

import (
	"errors"
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

func newInstallCommand(env *Env) *cobra.Command {
	var (
		title          string
		hidden         bool
		noAssets       bool
		fromSource     bool
		wantWidescreen bool
		all            bool
		onlyPS1        bool
		onlyPS2        bool
		fromList       string
	)

	cmd := &cobra.Command{
		Use:   "install [image...]",
		Short: "Install a PS1 or PS2 game onto the HDD",
		Long: `Install a disc image from anywhere on the filesystem.

  ps2hdd install ~/Downloads/sotc.iso
  ps2hdd install "Metal Gear Solid (Disc 1).cue" "Metal Gear Solid (Disc 2).cue"
  ps2hdd install --from-source "God Hand"
  ps2hdd install --all --dry-run
  ps2hdd install --from-list wanted.txt --dry-run

Naming several images installs them as one multi-disc PlayStation 1 title.

PS2 images are injected as HDLoader partitions with hdl_dump. PS1 images are
converted to the POPS VCD format and copied into the __.POPS partition.

Archives are read in place: a .7z, .zip or .rar holding a disc image can be
named directly, and only an install actually unpacks it.

A PS1 install also writes a POPStarter launcher under +OPL/APPS. OPL has no PS1
support of its own, so without one the game is on the disk and in no menu. A
multi-disc title also gets a DISCS.TXT so POPStarter can swap discs in game and
a VMCDIR.TXT so every disc shares one memory card.

--widescreen turns on POPStarter's GTE widescreen hack for a PS1 title. It
corrects 3D geometry and field of view; HUDs, fonts, menus and 2D backgrounds
stay stretched, and some games do not run with it. It is one line in a
CHEATS.TXT, so it can be changed afterwards without reinstalling.

--all plans the whole source library at once. Space is consumed as the plan
walks the list, so it answers the question a stack of single dry runs cannot:
not "does this title fit", which is yes for every title taken alone, but where
the run stops. PS2 titles are placed by replaying hdl_dump's allocator against
the drive's real chunk map; PS1 titles are counted against the room left inside
__.POPS, which is a different pool and runs out separately. Narrow it with
--ps1 or --ps2.

  ps2hdd install --all --dry-run          # everything
  ps2hdd install --all --ps1 --dry-run    # just the PlayStation 1 library

--from-list plans a chosen subset instead of everything. The file holds one
title, serial, filename or image path per line, so a directory listing works as
it stands:

  ls /mnt/roms/ps2 > wanted.txt

Blank lines are skipped and a '#' starts a comment, so the list can carry notes and live in version control. Every line
must resolve -- a typo that quietly dropped one game out of two hundred would
be noticed months later by its absence. A title containing a '#' is cut short
by the comment rule; name that one by its serial. The accounting is the same as
--all, so it answers where a curated run stops rather than whether each title
fits on its own.

  ps2hdd install --from-list wanted.txt --dry-run
  ps2hdd install --from-list wanted.txt --ps1 --dry-run

A directory of symbolic links is not an alternative: the source scanner reads
regular files only, so links are skipped without comment.

Add --dry-run to plan without writing. Without it the run installs everything
the plan says fits, and names the rest rather than trying them: a single
failure stops that title and nothing else, because across several hundred
titles one bad archive is close to certain and aborting for it would waste the
hours already spent. There is no resume state -- run the same command again and
titles already on the drive are skipped. Planning is the safe half; running a
several-hundred-title write has questions about ordering, resuming and partial
failure that are not settled yet.

The HDD is revalidated immediately before the write, whatever an earlier
command established.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			syncAssets := env.Config.Install.SyncAssets && !noAssets
			widescreen := env.Config.Install.Widescreen
			if cmd.Flags().Changed("widescreen") {
				widescreen = wantWidescreen
			}
			opts := app.InstallOptions{
				Title:      title,
				Hidden:     hidden,
				SyncAssets: syncAssets,
				Widescreen: widescreen,
			}

			if all && fromList != "" {
				return fmt.Errorf("--all and --from-list select different things; use one")
			}
			if all || fromList != "" {
				which := "--all"
				if fromList != "" {
					which = "--from-list"
				}
				if len(args) > 0 {
					return fmt.Errorf("%s chooses what to install; do not name images as well", which)
				}
				popts := app.PlanOptions{PS2: onlyPS2, PS1: onlyPS1}
				plan, err := buildPlan(env, ctx, fromList, popts)
				if err != nil {
					return err
				}
				if env.Svc.DryRun {
					return renderPlan(env, plan, nil)
				}
				if err := renderPlan(env, plan, nil); err != nil {
					return err
				}
				// One confirmation for the run, not one per title: a batch
				// that asked five hundred times would be answered by holding
				// down a key, which is not consent.
				if !env.Svc.AssumeYes && env.Config.TUI.ConfirmDestructiveActions {
					if !confirm(env.In, env.Out, "Install these?") {
						env.printf("Nothing was installed.\n")
						return nil
					}
				}
				bopts := opts
				bopts.Title = ""
				return runBatch(env, ctx, plan, bopts)
			}
			if len(args) == 0 {
				return fmt.Errorf("name a disc image, or use --all or --from-list to plan a set")
			}

			var games []model.Game
			if fromSource {
				// The catalog error is not discardable here: a library
				// that could not be read reports every title as available,
				// and this is the path that writes to the disk.
				c, _, err := env.Svc.Catalog(ctx)
				if err != nil {
					return err
				}
				for _, q := range args {
					g, err := resolveSource(c, q)
					if err != nil {
						return err
					}
					games = append(games, g)
				}
			} else {
				g, err := env.Svc.InspectSources(ctx, args, title)
				if err != nil {
					return err
				}
				games = []model.Game{g}
			}

			var reports []app.InstallReport
			for _, g := range games {
				if !env.Svc.DryRun && !env.Svc.AssumeYes && env.Config.TUI.ConfirmDestructiveActions {
					if !confirmInstall(env, g) {
						env.printf("Skipped %s.\n", g.Title)
						continue
					}
				}
				if !env.JSON {
					opts.OnProgress = progressPrinter(env, g.Title)
				}
				rep, err := env.Svc.Install(ctx, g, opts)
				if err != nil {
					if errors.Is(err, app.ErrAlreadyInstalled) {
						env.warnf("%s %v\n", amber("skipped:"), err)
						continue
					}
					return err
				}
				reports = append(reports, rep)
				if !env.JSON {
					reportInstall(env, rep)
				}
			}
			if env.JSON {
				return env.emitJSON(reports)
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.StringVar(&title, "title", "", "override the title shown in OPL")
	f.BoolVar(&hidden, "hidden", false, "hide a PS2 game from the PS2 HDD browser")
	f.BoolVar(&noAssets, "no-assets", false, "do not sync artwork after installing")
	f.BoolVar(&wantWidescreen, "widescreen", false,
		"turn on POPStarter's widescreen hack for a PS1 title (overrides install.widescreen)")
	f.BoolVar(&fromSource, "from-source", false, "treat the arguments as titles or IDs in the configured source directories")
	f.BoolVar(&all, "all", false, "plan every source title that is not installed (needs --dry-run)")
	f.StringVar(&fromList, "from-list", "", "plan the titles named in this file, one per line (needs --dry-run)")
	f.BoolVar(&onlyPS1, "ps1", false, "with --all, plan only PlayStation 1 titles")
	f.BoolVar(&onlyPS2, "ps2", false, "with --all, plan only PlayStation 2 titles")
	return cmd
}

// resolveSource finds a source-available title by name or id.
func resolveSource(c catalog.Catalog, query string) (model.Game, error) {
	e, err := resolveSourceEntry(c, query)
	if err != nil {
		return model.Game{}, err
	}
	// The source-side view is what an install acts on: it carries the image
	// path, where the installed view carries the partition.
	if e.SourceGame != nil {
		return *e.SourceGame, nil
	}
	return e.Game, nil
}

// resolveSourceEntry finds the catalog row for a query, which unlike the game
// alone still knows whether the title is on the drive.
func resolveSourceEntry(c catalog.Catalog, query string) (catalog.CatalogEntry, error) {
	matches := c.Find(query)
	var available []catalog.CatalogEntry
	for _, m := range matches {
		if m.AvailableInSource {
			available = append(available, m)
		}
	}
	switch len(available) {
	case 0:
		return catalog.CatalogEntry{}, fmt.Errorf("no source game matches %q", query)
	case 1:
		return available[0], nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d source games:", query, len(available))
		for _, m := range available {
			fmt.Fprintf(&b, "\n  %-4s %-14s %s", m.Platform.Label(), m.GameID, m.Title)
		}
		return catalog.CatalogEntry{}, errors.New(b.String() + "\n\nName one of them by its game ID.")
	}
}

func confirmInstall(env *Env, g model.Game) bool {
	env.printf("\n%s\n", bold("Install"))
	pairs := [][2]string{
		{"Title", g.Title},
		{"Platform", platformName(g.Platform)},
		{"ID", g.GameID},
		{"Size", model.HumanSize(g.SizeBytes)},
	}
	// The footprint is not the image's size: an APA partition is rounded up to
	// 128 MiB chunks with overhead on top, and a VCD gains a 1 MiB POPS
	// header. Showing it only when it differs keeps the common case quiet.
	if n := g.InstallSize(); n != g.SizeBytes {
		pairs = append(pairs, [2]string{"On HDD", model.HumanSize(n)})
	}
	if g.IsMultiDisc() {
		pairs = append(pairs, [2]string{"Discs", fmt.Sprintf("%d", g.DiscCount())})
	}
	if g.Media != model.MediaUnknown {
		pairs = append(pairs, [2]string{"Media", strings.ToUpper(string(g.Media))})
	}
	pairs = append(pairs, [2]string{"Device", env.Config.Device})
	kv(env.Out, pairs)
	return confirm(env.In, env.Out, "Proceed?")
}

// progressPrinter renders stage changes on one line, redrawing in place.
//
// A stage that reports no percentage prints the stage name only: a fake
// percentage would be worse than none. When output is not a terminal the
// in-place redraw is dropped and only stage changes are printed, so a log file
// does not fill with escape sequences.
func progressPrinter(env *Env, title string) app.ProgressFunc {
	last := ""
	interactive := colorEnabled
	return func(p app.Progress) {
		line := stageLabel(p.Stage)
		if !p.Indeterminate() {
			line = fmt.Sprintf("%s %3.0f%%", line, p.Fraction*100)
		}
		if line == last {
			return
		}
		if !interactive {
			// Only announce a change of stage; percentages would be noise.
			if stageLabel(p.Stage) == stageOf(last) {
				last = line
				return
			}
			last = line
			fmt.Fprintf(env.Out, "  %s %s\n", title, stageLabel(p.Stage))
			return
		}
		last = line
		fmt.Fprintf(env.Out, "\r\033[K  %s %s", dim(title), line)
		if p.Stage == app.StageComplete {
			fmt.Fprintln(env.Out)
		}
	}
}

// stageOf strips the trailing percentage from a rendered progress line.
func stageOf(line string) string {
	if i := strings.LastIndex(line, " "); i > 0 && strings.HasSuffix(line, "%") {
		return strings.TrimSpace(line[:i])
	}
	return line
}

func stageLabel(s app.Stage) string {
	switch s {
	case app.StageValidating:
		return "checking the HDD"
	case app.StageInspecting:
		return "inspecting"
	case app.StageConverting:
		return "converting to VCD"
	case app.StageInstalling:
		return "installing"
	case app.StageRemoving:
		return "removing"
	case app.StageVerifying:
		return "verifying"
	case app.StageSyncAssets:
		return "syncing artwork"
	case app.StageComplete:
		return green("complete")
	default:
		return string(s)
	}
}

func reportInstall(env *Env, rep app.InstallReport) {
	if rep.DryRun {
		env.printf("\n%s %s (%s)\n", bold("Would install"), rep.Game.Title, rep.Game.GameID)
		for _, c := range rep.Commands {
			env.printf("  %s %s\n", dim("$"), strings.Join(c, " "))
		}
		for _, f := range rep.Files {
			env.printf("  %s write %s\n", dim("$"), f)
		}
		return
	}
	env.printf("Installed %s (%s).\n", bold(rep.Game.Title), rep.Game.GameID)
	if rep.AssetsInstalled > 0 {
		env.printf("  %d artwork file(s) installed.\n", rep.AssetsInstalled)
	}
	for _, w := range rep.Warnings {
		env.printf("  %s %s\n", amber("!"), w)
	}
}
