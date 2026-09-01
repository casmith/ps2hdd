package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

// RemoveBatchReport is the outcome of removing a list of titles.
type RemoveBatchReport struct {
	Removed []model.Game `json:"removed,omitempty"`
	// Failed carries a message per title. One title that will not delete is
	// not a reason to leave the rest of the list on the drive.
	Failed []BatchFailure `json:"failed,omitempty"`
	// NotInstalled counts lines naming a title that is not on the drive.
	// Removing something already gone is a no-op, not a mistake, which is why
	// it is counted rather than refused.
	NotInstalled int `json:"not_installed,omitempty"`
	// FreedBytes is what the removals actually reclaimed.
	FreedBytes int64 `json:"freed_bytes,omitempty"`
	Cancelled  bool  `json:"cancelled,omitempty"`
}

// removeList removes every installed title named in a file.
//
// The same list that installed a set of games should be able to take them off
// again, so the entries are resolved the same way: a title, a serial, or the
// filename of the archive it came from. A directory listing is the obvious way
// to build one of these and it works for both directions.
//
// Every line must resolve to something known. A line naming a title that is
// simply not installed is counted and skipped -- deleting what is already gone
// is a no-op -- but a line that matches nothing at all stops the run before
// anything is deleted. On a delete list a typo is worth much more care than on
// an install list: the wrong match takes a game off the drive.
func removeList(env *Env, ctx context.Context, path string, opts app.RemoveOptions) error {
	lines, err := readGameList(path)
	if err != nil {
		return err
	}
	c, warnings, err := env.Svc.Catalog(ctx)
	if err != nil {
		return err
	}
	warn(env, warnings)

	var todo []model.Game
	var unresolved []string
	var rep RemoveBatchReport
	seen := map[string]bool{}
	for _, e := range lines {
		g, installed, err := resolveInstalledEntry(env, ctx, c, e.Query)
		if err != nil {
			unresolved = append(unresolved, fmt.Sprintf("line %d: %s", e.Line, err))
			continue
		}
		if !installed {
			rep.NotInstalled++
			continue
		}
		if seen[g.Key()] {
			continue
		}
		seen[g.Key()] = true
		todo = append(todo, g)
	}
	if len(unresolved) > 0 {
		return fmt.Errorf("%d entr(ies) in %s could not be resolved, and nothing was removed:\n  %s",
			len(unresolved), path, strings.Join(unresolved, "\n  "))
	}

	if len(todo) == 0 {
		env.printf("%s\n", dim(fmt.Sprintf(
			"Nothing to remove: %d title(s) named are not on the drive.", rep.NotInstalled)))
		return nil
	}

	if err := renderRemovePlan(env, todo, rep.NotInstalled, opts.PurgeAssets); err != nil {
		return err
	}
	if env.Svc.DryRun {
		return nil
	}
	// One confirmation for the run. Asking per title would be answered by
	// holding down a key, which is not consent -- and this is the destructive
	// direction, where that matters most.
	if !env.Svc.AssumeYes && env.Config.TUI.ConfirmDestructiveActions {
		if !confirm(env.In, env.Out, "This cannot be undone. Remove them?") {
			env.printf("Nothing was removed.\n")
			return nil
		}
	}

	for i, g := range todo {
		if ctx.Err() != nil {
			rep.Cancelled = true
			break
		}
		if !env.JSON {
			env.printf("\n%s %s\n", dim(fmt.Sprintf("[%d/%d]", i+1, len(todo))), bold(g.Title))
			opts.OnProgress = progressPrinter(env, g.Title)
		}
		r, err := env.Svc.Remove(ctx, g, opts)
		switch {
		case errors.Is(err, context.Canceled):
			rep.Cancelled = true
		case err != nil:
			rep.Failed = append(rep.Failed, BatchFailure{Title: g.Title, Err: firstLine(err.Error())})
			env.warnf("  %s %s\n", amber("failed:"), firstLine(err.Error()))
		default:
			rep.Removed = append(rep.Removed, g)
			rep.FreedBytes += g.InstallSize()
			_ = r
		}
		if rep.Cancelled {
			break
		}
	}

	if env.JSON {
		return env.emitJSON(rep)
	}
	renderRemoveOutcome(env, rep, len(todo))
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d of %d title(s) failed to remove", len(rep.Failed), len(todo))
	}
	return nil
}

// resolveInstalledEntry turns one line into a title, saying whether it is on
// the drive.
//
// A serial, a title or a partition name resolves against the installed library
// directly. A filename does not -- an installed game has no source path -- so
// it goes through the catalog, which is what pairs an installed title with the
// archive it came from.
func resolveInstalledEntry(env *Env, ctx context.Context, c catalog.Catalog, query string) (model.Game, bool, error) {
	if g, err := env.Svc.FindInstalled(ctx, query); err == nil {
		return g, true, nil
	} else if isAmbiguous(err) {
		return model.Game{}, false, err
	}
	if m := c.FindSourceFile(query); len(m) == 1 {
		return m[0].Game, m[0].Installed, nil
	} else if len(m) > 1 {
		return model.Game{}, false, ambiguous(query, m)
	}
	e, err := resolveSourceEntry(c, query)
	if err != nil {
		return model.Game{}, false, fmt.Errorf("nothing installed or known matches %q", query)
	}
	return e.Game, e.Installed, nil
}

func isAmbiguous(err error) bool {
	var a *app.AmbiguousError
	return errors.As(err, &a)
}

func renderRemovePlan(env *Env, todo []model.Game, notInstalled int, purge bool) error {
	freed := freedBy(todo)
	if env.JSON {
		return env.emitJSON(map[string]any{
			"would_remove": todo, "freed_bytes": freed, "not_installed": notInstalled,
		})
	}
	env.printf("\n%s %d title(s)\n", bold(red("Would remove")), len(todo))
	if notInstalled > 0 {
		env.printf("  %s\n", dim(fmt.Sprintf("%d named but not on the drive, skipped", notInstalled)))
	}
	for _, g := range todo {
		discs := ""
		if g.IsMultiDisc() {
			discs = dim(fmt.Sprintf("  (%d discs)", g.DiscCount()))
		}
		env.printf("  %s %-13s %-44s %s%s\n", dim(g.Platform.Label()), g.GameID,
			components1(g.Title, 44), dim(model.HumanSize(g.InstallSize())), discs)
	}
	pairs := [][2]string{
		{"Frees", model.HumanSize(freed)},
		{"Device", env.Config.Device},
	}
	if purge {
		pairs = append(pairs, [2]string{"Artwork", amber("will also be deleted")})
	} else {
		pairs = append(pairs, [2]string{"Artwork", "kept"})
	}
	kv(env.Out, pairs)
	return nil
}

func renderRemoveOutcome(env *Env, rep RemoveBatchReport, attempted int) {
	section(env.Out, "Done")
	pairs := [][2]string{
		{"Removed", fmt.Sprintf("%d of %d", len(rep.Removed), attempted)},
		{"Freed", model.HumanSize(rep.FreedBytes)},
	}
	if len(rep.Failed) > 0 {
		pairs = append(pairs, [2]string{"Failed", amber(fmt.Sprintf("%d", len(rep.Failed)))})
	}
	if rep.NotInstalled > 0 {
		pairs = append(pairs, [2]string{"Not on the drive",
			dim(fmt.Sprintf("%d, skipped", rep.NotInstalled))})
	}
	if rep.Cancelled {
		pairs = append(pairs, [2]string{"Stopped", amber("interrupted")})
	}
	kv(env.Out, pairs)
	for _, f := range rep.Failed {
		env.printf("  %s %-40s %s\n", amber("!"), f.Title, dim(f.Err))
	}
}

// freedBy totals what removing these titles reclaims. It is the footprint, not
// the file size: what comes back is the space the drive gave them.
func freedBy(games []model.Game) int64 {
	var n int64
	for _, g := range games {
		n += g.InstallSize()
	}
	return n
}

// components1 truncates a title for the plan table.
func components1(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}
