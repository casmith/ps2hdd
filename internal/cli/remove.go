package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/model"
)

func newRemoveCommand(env *Env) *cobra.Command {
	var purgeAssets bool
	var fromList string

	cmd := &cobra.Command{
		Use:     "remove [game-id|title...]",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Remove an installed game from the HDD",
		Long: `Remove an installed PS1 or PS2 title.

  ps2hdd remove SLUS_209.46
  ps2hdd remove "Shadow of the Colossus"

A name that matches more than one title is refused rather than guessed at.
Artwork is kept unless --purge-assets is given: it is small, often
hand-curated, and exactly what you want back if you reinstall.

Removing a multi-disc PS1 title removes every disc.

--from-list removes everything named in a file, resolved the same way an
install list is: a title, a serial, a partition name, or the filename of the
archive it came from. The same list that put a set of games on the drive takes
them off again.

  ps2hdd remove --from-list gone.txt --dry-run
  ps2hdd remove --from-list gone.txt

Every line must resolve to something known, and nothing is removed if any line
does not -- on a delete list a typo costs more than on an install list. A line
naming a title that is simply not installed is counted and skipped, because
deleting what is already gone is a no-op. Confirmation is once for the run.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()

			if fromList != "" {
				if len(args) > 0 {
					return fmt.Errorf("--from-list chooses what to remove; do not name titles as well")
				}
				return removeList(env, ctx, config.ExpandPath(fromList),
					app.RemoveOptions{PurgeAssets: purgeAssets})
			}
			if len(args) == 0 {
				return fmt.Errorf("name a title to remove, or use --from-list")
			}
			var reports []app.RemoveReport

			for _, query := range args {
				g, err := env.Svc.FindInstalled(ctx, query)
				if err != nil {
					return err
				}
				if !env.Svc.DryRun && !env.Svc.AssumeYes && env.Config.TUI.ConfirmDestructiveActions {
					if !confirmRemove(env, g, purgeAssets) {
						env.printf("Kept %s.\n", g.Title)
						continue
					}
				}
				opts := app.RemoveOptions{PurgeAssets: purgeAssets}
				if !env.JSON {
					opts.OnProgress = progressPrinter(env, g.Title)
				}
				rep, err := env.Svc.Remove(ctx, g, opts)
				if err != nil {
					return err
				}
				reports = append(reports, rep)
				if !env.JSON {
					reportRemove(env, rep)
				}
			}
			if env.JSON {
				return env.emitJSON(reports)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&purgeAssets, "purge-assets", false, "also delete the game's artwork and configuration")
	cmd.Flags().StringVar(&fromList, "from-list", "", "remove the titles named in this file, one per line")
	return cmd
}

func confirmRemove(env *Env, g model.Game, purge bool) bool {
	env.printf("\n%s\n", bold(red("Remove")))
	pairs := [][2]string{
		{"Title", g.Title},
		{"Platform", platformName(g.Platform)},
		{"ID", g.GameID},
		{"Size", model.HumanSize(g.SizeBytes)},
	}
	if g.IsMultiDisc() {
		pairs = append(pairs, [2]string{"Discs", fmt.Sprintf("%d (all will be removed)", g.DiscCount())})
	}
	if g.PartitionName != "" {
		pairs = append(pairs, [2]string{"On HDD", g.PartitionName})
	}
	pairs = append(pairs, [2]string{"Device", env.Config.Device})
	if purge {
		pairs = append(pairs, [2]string{"Artwork", amber("will also be deleted")})
	} else {
		pairs = append(pairs, [2]string{"Artwork", "kept"})
	}
	kv(env.Out, pairs)
	return confirm(env.In, env.Out, "This cannot be undone. Proceed?")
}

func reportRemove(env *Env, rep app.RemoveReport) {
	if rep.DryRun {
		env.printf("\n%s %s (%s)\n", bold("Would remove"), rep.Game.Title, rep.Game.GameID)
		for _, c := range rep.Commands {
			env.printf("  %s %s\n", dim("$"), strings.Join(c, " "))
		}
		for _, f := range rep.Files {
			env.printf("  %s delete %s\n", dim("$"), f)
		}
		for _, a := range rep.Assets {
			env.printf("  %s delete %s\n", dim("$"), a)
		}
		return
	}
	env.printf("Removed %s (%s).\n", bold(rep.Game.Title), rep.Game.GameID)
	if len(rep.Assets) > 0 {
		env.printf("  %d artwork file(s) deleted.\n", len(rep.Assets))
	}
}
