package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/asset"
	"github.com/casmith/ps2hdd/internal/model"
)

// newArtCommand and newAssetsCommand share their implementations: `art` is the
// artwork-only spelling users reach for, `assets` covers artwork plus the
// per-game OPL configuration.
func newArtCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "art",
		Short: "Inspect and populate OPL artwork",
		Long: `Artwork lives in +OPL/ART, named <serial>_<TYPE>.png. ps2hdd never
overwrites an existing file unless you ask it to: artwork is often hand-picked
and OPL gives no way to get it back.`,
	}
	cmd.AddCommand(newAssetStatusCommand(env, true), newAssetSyncCommand(env, true))
	return cmd
}

func newAssetsCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "assets",
		Short: "Inspect and populate OPL artwork and per-game configuration",
	}
	cmd.AddCommand(
		newAssetStatusCommand(env, false),
		newAssetSyncCommand(env, false),
		newAssetsCleanCommand(env),
	)
	return cmd
}

func newAssetStatusCommand(env *Env, artOnly bool) *cobra.Command {
	var missingOnly bool
	cmd := &cobra.Command{
		Use:   "status",
		Short: "Show which artwork each installed game has",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			rows, err := env.Svc.AssetStatus(cmd.Context(), nil)
			if err != nil {
				return err
			}
			want := env.Svc.Config.WantedAssets()
			if artOnly {
				want = artOnlyTypes(want)
			}
			if missingOnly {
				var kept []asset.StatusRow
				for _, r := range rows {
					if len(missingOf(r, want)) > 0 {
						kept = append(kept, r)
					}
				}
				rows = kept
			}
			if env.JSON {
				return env.emitJSON(rows)
			}
			if len(rows) == 0 {
				env.printf("Nothing to report.\n")
				return nil
			}

			t := newTable(env.Out)
			header := []string{"GAME", "ID"}
			for _, a := range want {
				header = append(header, string(a))
			}
			fmt.Fprintln(t, bold(strings.Join(header, "\t")))
			for _, r := range rows {
				cells := []string{truncate(r.Game.Title, 40), r.Game.GameID}
				for _, a := range want {
					cells = append(cells, mark(r.Present[a]))
				}
				fmt.Fprintln(t, strings.Join(cells, "\t"))
			}
			t.Flush()

			complete := 0
			for _, r := range rows {
				if len(missingOf(r, want)) == 0 {
					complete++
				}
			}
			env.printf("\n%d of %d games have every enabled slot.\n", complete, len(rows))
			return nil
		},
	}
	cmd.Flags().BoolVarP(&missingOnly, "missing", "m", false, "only games with something missing")
	return cmd
}

func newAssetSyncCommand(env *Env, artOnly bool) *cobra.Command {
	var (
		overwrite bool
		all       bool
	)
	cmd := &cobra.Command{
		Use:   "sync [game-id|title...]",
		Short: "Download and install missing artwork",
		Long: `Fetch artwork for the named games, or with --all for every installed game.

Existing files are left alone unless --overwrite is given.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := cmd.Context()
			if len(args) == 0 && !all {
				return fmt.Errorf("name at least one game, or pass --all")
			}

			var games []model.Game
			if all {
				var err error
				if games, err = env.Svc.Installed(ctx); err != nil {
					return err
				}
			} else {
				for _, q := range args {
					g, err := env.Svc.FindInstalled(ctx, q)
					if err != nil {
						return err
					}
					games = append(games, g)
				}
			}
			if len(games) == 0 {
				env.printf("No installed games.\n")
				return nil
			}

			opts := app.SyncAssetsOptions{Overwrite: overwrite}
			if !env.JSON {
				opts.OnProgress = func(done, total int, item asset.PlanItem) {
					fmt.Fprintf(env.Out, "\r\033[K  %s %d/%d %s %s",
						dim("fetching"), done, total, item.Game.GameID, item.Type)
				}
			}
			plan, res, err := env.Svc.SyncAssets(ctx, games, opts)
			if !env.JSON {
				fmt.Fprint(env.Out, "\r\033[K")
			}
			if err != nil {
				return err
			}

			if env.JSON {
				return env.emitJSON(map[string]any{"plan": plan, "result": res, "dry_run": env.Svc.DryRun})
			}
			if env.Svc.DryRun {
				env.printf("%s\n", bold("Would install"))
				for _, it := range plan.Items {
					env.printf("  %-14s %-5s %s\n", it.Game.GameID, it.Type, dim(it.Asset.Source))
				}
				if len(plan.Items) == 0 {
					env.printf("  %s\n", dim("nothing (every enabled slot is already present)"))
				}
			} else {
				env.printf("Installed %d artwork file(s)", len(res.Installed))
				if res.Bytes > 0 {
					env.printf(" (%s)", model.HumanSize(res.Bytes))
				}
				env.printf(".\n")
			}
			for _, f := range res.Failed {
				env.warnf("  %s %s %s: %s\n", red("failed"), f.Game.GameID, f.Type, f.Reason)
			}
			if n := len(plan.Unavailable); n > 0 {
				env.printf("\n%s\n", amber(fmt.Sprintf("%d slot(s) are missing and no configured provider has them:", n)))
				byType := map[model.AssetType]int{}
				for _, u := range plan.Unavailable {
					byType[u.Type]++
				}
				for _, t := range model.ArtTypes {
					if byType[t] > 0 {
						env.printf("  %-5s %d game(s)\n", t, byType[t])
					}
				}
				env.printf("\n%s\n", dim("Point [assets] mirror at a local artwork collection, or add [assets.templates]"))
				env.printf("%s\n", dim("entries for another database. See docs/compatibility.md."))
			}
			return nil
		},
	}
	f := cmd.Flags()
	f.BoolVar(&overwrite, "overwrite", false, "replace artwork that is already on the HDD")
	f.BoolVarP(&all, "all", "a", false, "sync every installed game")
	return cmd
}

func newAssetsCleanCommand(env *Env) *cobra.Command {
	return &cobra.Command{
		Use:   "clean",
		Short: "Empty the artwork download cache",
		Long:  "The cache is disposable; clearing it only costs bandwidth on the next sync.",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			n, size, err := env.Svc.CleanAssetCache()
			if err != nil {
				return err
			}
			if env.JSON {
				return env.emitJSON(map[string]any{"removed": n, "bytes": size})
			}
			env.printf("Removed %d cached file(s) (%s).\n", n, model.HumanSize(size))
			return nil
		},
	}
}

func newDatabaseCommand(env *Env) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "database",
		Short: "Manage the artwork provider's local data",
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "update",
		Short: "Refresh the artwork provider and report its reachability",
		Long: `ps2hdd's artwork providers resolve URLs on demand rather than keeping a
downloaded index, so there is no database to rebuild. This command checks that
the configured provider answers and clears any stale cached downloads that a
provider change would otherwise mask.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := env.Svc.AssetProvider()
			if err != nil {
				return err
			}
			checkErr := p.Check(cmd.Context())
			n, size, cleanErr := env.Svc.CleanAssetCache()
			if cleanErr != nil {
				return cleanErr
			}
			if env.JSON {
				return env.emitJSON(map[string]any{
					"provider":      p.Name(),
					"ok":            checkErr == nil,
					"error":         errString(checkErr),
					"cache_removed": n,
					"cache_bytes":   size,
				})
			}
			env.printf("Provider: %s\n", p.Name())
			if checkErr != nil {
				env.printf("Status:   %s (%v)\n", amber("unreachable"), checkErr)
			} else {
				env.printf("Status:   %s\n", green("reachable"))
			}
			env.printf("Cache:    cleared %d file(s), %s\n", n, model.HumanSize(size))
			return checkErr
		},
	})
	return cmd
}

func artOnlyTypes(want []model.AssetType) []model.AssetType {
	out := make([]model.AssetType, 0, len(want))
	for _, t := range want {
		if t.IsArt() {
			out = append(out, t)
		}
	}
	return out
}

func missingOf(r asset.StatusRow, want []model.AssetType) []model.AssetType {
	var out []model.AssetType
	for _, t := range want {
		if !r.Present[t] {
			out = append(out, t)
		}
	}
	return out
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
