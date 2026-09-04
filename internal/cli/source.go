package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

func newSourceCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "source",
		Short: "Work with the configured source directories",
		Long: `The source directories are places to browse for installable disc images.
They are never treated as a record of what is installed: only the HDD decides
that.`,
	}
	cmd.AddCommand(newSourceScanCommand(env), newSourceListCommand(env))
	return cmd
}

func newSourceScanCommand(env *Env) *cobra.Command {
	var rescan bool
	cmd := &cobra.Command{
		Use:   "scan",
		Short: "Scan the source directories and cache the results",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			if rescan {
				if err := env.Svc.ClearSourceCache(); err != nil {
					env.warnf("%s could not clear the scan cache: %v\n", amber("warning:"), err)
				}
			}
			opts, finish := withScanProgress(env)
			ps2Res, ps1Res, err := env.Svc.ScanSourcesWith(cmd.Context(), opts)
			finish()
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(map[string]catalog.ScanResult{"ps2": ps2Res, "ps1": ps1Res})
			}
			for _, r := range []struct {
				label string
				res   catalog.ScanResult
			}{{"PS2", ps2Res}, {"PS1", ps1Res}} {
				if r.res.Root == "" {
					env.printf("%s sources: %s\n", r.label, dim("not configured"))
					continue
				}
				env.printf("%s sources: %s\n", r.label, r.res.Root)
				env.printf("  %d titles from %d files (%d served from cache)\n",
					len(r.res.Games), r.res.Scanned, r.res.Cached)
				if n := len(r.res.Problems); n > 0 {
					env.printf("  %s\n", amber(fmt.Sprintf("%d file(s) could not be identified", n)))
				}
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&rescan, "rescan", false, "ignore cached metadata and re-read every image")
	return cmd
}

func newSourceListCommand(env *Env) *cobra.Command {
	var onlyPS1, onlyPS2, showProblems bool
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List the games found in the source directories",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			opts, finish := withScanProgress(env)
			ps2Res, ps1Res, err := env.Svc.ScanSourcesWith(cmd.Context(), opts)
			finish()
			if err != nil {
				return err
			}
			var games []model.Game
			if !onlyPS1 {
				games = append(games, ps2Res.Games...)
			}
			if !onlyPS2 {
				games = append(games, ps1Res.Games...)
			}
			model.SortGames(games)
			problems := append(append([]catalog.ScanProblem{}, ps2Res.Problems...), ps1Res.Problems...)

			if env.JSON {
				return env.emitJSON(map[string]any{"games": games, "problems": problems})
			}
			entries := make([]catalog.CatalogEntry, 0, len(games))
			for _, g := range games {
				entries = append(entries, catalog.CatalogEntry{Game: g, AvailableInSource: true})
			}
			renderGames(env.Out, entries, false)
			if showProblems && len(problems) > 0 {
				section(env.Out, fmt.Sprintf("Unidentified files (%d)", len(problems)))
				for _, p := range problems {
					env.printf("  %s\n    %s\n", p.Path, dim(p.Reason))
				}
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&onlyPS1, "ps1", false, "only PlayStation 1 titles")
	f.BoolVar(&onlyPS2, "ps2", false, "only PlayStation 2 titles")
	f.BoolVar(&showProblems, "problems", false, "also list files that could not be identified")
	return cmd
}
