package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/model"
)

func newRemoveCommand(env *Env) *cobra.Command {
	var purgeAssets bool

	cmd := &cobra.Command{
		Use:     "remove <game-id|title>",
		Aliases: []string{"rm", "uninstall"},
		Short:   "Remove an installed game from the HDD",
		Long: `Remove an installed PS1 or PS2 title.

  ps2hdd remove SLUS_209.46
  ps2hdd remove "Shadow of the Colossus"

A name that matches more than one title is refused rather than guessed at.
Artwork is kept unless --purge-assets is given: it is small, often
hand-curated, and exactly what you want back if you reinstall.

Removing a multi-disc PS1 title removes every disc.`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
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
