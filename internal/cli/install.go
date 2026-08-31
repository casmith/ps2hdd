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
		title      string
		hidden     bool
		noAssets   bool
		fromSource bool
	)

	cmd := &cobra.Command{
		Use:   "install <image> [image...]",
		Short: "Install a PS1 or PS2 game onto the HDD",
		Long: `Install a disc image from anywhere on the filesystem.

  ps2hdd install ~/Downloads/sotc.iso
  ps2hdd install "Metal Gear Solid (Disc 1).cue" "Metal Gear Solid (Disc 2).cue"
  ps2hdd install --from-source "God Hand"

Naming several images installs them as one multi-disc PlayStation 1 title.

PS2 images are injected as HDLoader partitions with hdl_dump. PS1 images are
converted to the POPS VCD format and copied into the __.POPS partition; a
multi-disc title also gets a DISCS.TXT so POPStarter can swap discs in game.

A PS1 install also writes a POPStarter launcher under +OPL/APPS. OPL has no PS1
support of its own, so without one the game is on the disk and in no menu.

The HDD is revalidated immediately before the write, whatever an earlier
command established.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

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
				g, err := env.Svc.InspectSources(args, title)
				if err != nil {
					return err
				}
				games = []model.Game{g}
			}

			syncAssets := env.Config.Install.SyncAssets && !noAssets
			opts := app.InstallOptions{
				Title:      title,
				Hidden:     hidden,
				SyncAssets: syncAssets,
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
	f.BoolVar(&fromSource, "from-source", false, "treat the arguments as titles or IDs in the configured source directories")
	return cmd
}

// resolveSource finds a source-available title by name or id.
func resolveSource(c catalog.Catalog, query string) (model.Game, error) {
	matches := c.Find(query)
	var available []catalog.CatalogEntry
	for _, m := range matches {
		if m.AvailableInSource {
			available = append(available, m)
		}
	}
	switch len(available) {
	case 0:
		return model.Game{}, fmt.Errorf("no source game matches %q", query)
	case 1:
		g := available[0].Game
		if available[0].SourceGame != nil {
			g = *available[0].SourceGame
		}
		return g, nil
	default:
		var b strings.Builder
		fmt.Fprintf(&b, "%q matches %d source games:", query, len(available))
		for _, m := range available {
			fmt.Fprintf(&b, "\n  %-4s %-14s %s", m.Platform.Label(), m.GameID, m.Title)
		}
		return model.Game{}, errors.New(b.String() + "\n\nName one of them by its game ID.")
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
