package cli

import (
	"context"
	"fmt"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/model"
)

// planAll renders what installing the whole source library would cost.
func planAll(env *Env, ctx context.Context, opts app.PlanOptions) error {
	plan, warnings, err := env.Svc.PlanInstallAll(ctx, opts)
	if err != nil {
		return err
	}
	if env.JSON {
		return env.emitJSON(plan)
	}
	for _, w := range warnings {
		env.printf("%s %s\n", amber("!"), w)
	}

	total := plan.PS2.Count() + plan.PS1.Count()
	if total == 0 {
		env.printf("%s\n", dim("Nothing to install: every source title is already on the HDD."))
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
