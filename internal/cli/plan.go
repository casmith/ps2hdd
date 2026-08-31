package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/catalog"
	"github.com/casmith/ps2hdd/internal/model"
)

// planAll renders what installing the whole source library would cost.
func planAll(env *Env, ctx context.Context, opts app.PlanOptions) error {
	plan, warnings, err := env.Svc.PlanInstallAll(ctx, opts)
	if err != nil {
		return err
	}
	return renderPlan(env, plan, warnings)
}

// planList renders what installing the titles named in a file would cost.
//
// Every line must resolve. A list is usually generated or hand-edited, and a
// typo that quietly dropped one game out of two hundred would be found months
// later by its absence -- so an unresolved line stops the plan and is reported
// by line number, with the whole set of failures at once rather than the first.
func planList(env *Env, ctx context.Context, path string, opts app.PlanOptions) error {
	lines, err := readGameList(path)
	if err != nil {
		return err
	}
	c, warnings, err := env.Svc.Catalog(ctx)
	if err != nil {
		return err
	}

	var games []model.Game
	var unresolved []string
	seen := map[string]bool{}
	skipped := 0
	for _, e := range lines {
		g, installed, err := resolveListEntry(env, ctx, c, e.Query)
		if err != nil {
			unresolved = append(unresolved, fmt.Sprintf("line %d: %s", e.Line, err))
			continue
		}
		// A list that names the same game twice would otherwise have it
		// counted twice against the free space.
		key := model.NormalizeGameID(g.GameID) + "|" + g.Title
		if seen[key] {
			continue
		}
		seen[key] = true
		if installed {
			skipped++
			continue
		}
		games = append(games, g)
	}
	if len(unresolved) > 0 {
		return fmt.Errorf("%d entr(ies) in %s could not be resolved:\n  %s",
			len(unresolved), path, strings.Join(unresolved, "\n  "))
	}

	plan, err := env.Svc.PlanInstall(ctx, games, opts)
	if err != nil {
		return err
	}
	plan.Skipped = skipped
	return renderPlan(env, plan, warnings)
}

// resolveListEntry turns one line into a title: a path if it names a file that
// exists, and a catalog lookup otherwise. Both shapes are useful in one list --
// a library title by name, a one-off image by path.
//
// installed is read from the catalog row rather than from the game, because a
// title that is both on the drive and in a source directory resolves to its
// source-side view, whose Installed is false by construction. Counting one of
// those against the free space would make a plan for a mostly-installed
// library ask for room it does not need.
func resolveListEntry(env *Env, ctx context.Context, c catalog.Catalog, query string) (model.Game, bool, error) {
	if looksLikePath(query) {
		if _, err := os.Stat(query); err == nil {
			g, err := env.Svc.InspectSource(ctx, query)
			if err != nil {
				return model.Game{}, false, err
			}
			return g, installedInCatalog(c, g.GameID), nil
		}
	}
	// A filename is tried before a title. A list is very often a directory
	// listing, and "Ace Combat 04 (USA).7z" is neither a title -- the
	// extension is not part of one -- nor a path, since it names no directory.
	if m := c.FindSourceFile(query); len(m) == 1 {
		return sourceView(m[0]), m[0].Installed, nil
	} else if len(m) > 1 {
		return model.Game{}, false, ambiguous(query, m)
	}
	e, err := resolveSourceEntry(c, query)
	if err != nil {
		return model.Game{}, false, err
	}
	return sourceView(e), e.Installed, nil
}

// sourceView is the side of an entry an install acts on: the image path rather
// than the partition.
func sourceView(e catalog.CatalogEntry) model.Game {
	if e.SourceGame != nil {
		return *e.SourceGame
	}
	return e.Game
}

// ambiguous reports a query that names more than one title.
func ambiguous(query string, matches []catalog.CatalogEntry) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%q matches %d source games:", query, len(matches))
	for _, m := range matches {
		fmt.Fprintf(&b, "\n      %-4s %-14s %s", m.Platform.Label(), m.GameID, m.Title)
	}
	return errors.New(b.String())
}

// installedInCatalog reports whether a serial is already on the drive.
func installedInCatalog(c catalog.Catalog, gameID string) bool {
	want := model.NormalizeGameID(gameID)
	if want == "" {
		return false
	}
	for _, e := range c.Entries {
		if e.Installed && model.NormalizeGameID(e.GameID) == want {
			return true
		}
	}
	return false
}

func renderPlan(env *Env, plan app.InstallPlan, warnings []error) error {
	if env.JSON {
		return env.emitJSON(plan)
	}
	for _, w := range warnings {
		env.printf("%s %s\n", amber("!"), w)
	}

	total := plan.PS2.Count() + plan.PS1.Count()
	if total == 0 {
		env.printf("%s\n", dim("Nothing to install: every title asked for is already on the HDD."))
		return nil
	}
	env.printf("\n%s %d title(s)\n", bold("Would install"), total)
	if plan.Skipped > 0 {
		env.printf("  %s\n", dim(fmt.Sprintf("%d already installed, skipped", plan.Skipped)))
	}

	for _, p := range []app.PlatformPlan{plan.PS2, plan.PS1} {
		if p.Count() == 0 {
			continue
		}
		renderPlatformPlan(env, p)
	}

	if !plan.Fits() {
		env.printf("\n%s\n", amber("This library does not fit as it stands."))
	}
	return nil
}

func renderPlatformPlan(env *Env, p app.PlatformPlan) {
	section(env.Out, platformName(p.Platform))
	fitCount, fitBytes := p.Fitting()
	overCount, overBytes := p.Excess()

	pairs := [][2]string{
		{"Titles", fmt.Sprintf("%d", p.Count())},
		{"Needs", model.HumanSize(p.Bytes())},
	}
	// Without a measured figure, "free" would be zero and every verdict drawn
	// from it a fiction. Saying so is the honest row.
	if !p.FreeKnown {
		pairs = append(pairs,
			[2]string{"Free in " + p.Where, dim("not measured")},
			[2]string{"Verdict", dim("unknown; attach the drive to find out")})
		kv(env.Out, pairs)
		return
	}
	pairs = append(pairs, [2]string{"Free in " + p.Where, model.HumanSize(p.FreeBytes)})
	if overCount == 0 {
		pairs = append(pairs, [2]string{"Verdict",
			green(fmt.Sprintf("fits, %s to spare", model.HumanSize(p.FreeBytes-fitBytes)))})
	} else {
		pairs = append(pairs,
			[2]string{"Fits", fmt.Sprintf("%d title(s), %s", fitCount, model.HumanSize(fitBytes))},
			[2]string{"Verdict",
				amber(fmt.Sprintf("%d title(s) do not fit; %s short", overCount, model.HumanSize(overBytes)))})
	}
	kv(env.Out, pairs)

	if overCount > 0 {
		// The titles that do not fit are the actionable half, so they are the
		// ones listed. Printing five hundred that do would bury them.
		env.printf("\n  %s\n", dim("Will not fit:"))
		shown := 0
		for _, e := range p.Entries {
			if e.Fits {
				continue
			}
			if shown == maxListedProblems {
				env.printf("  %s\n", dim(fmt.Sprintf("… and %d more", overCount-shown)))
				break
			}
			env.printf("  %s  %s\n",
				dim(model.HumanSize(e.Bytes)), e.Game.Title)
			shown++
		}
	}
}
