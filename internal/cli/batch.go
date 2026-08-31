package cli

import (
	"context"
	"errors"
	"fmt"

	"github.com/casmith/ps2hdd/internal/app"
	"github.com/casmith/ps2hdd/internal/model"
)

// splitPlan separates the titles a plan says will fit from the rest.
//
// The ones that will not are named rather than tried. The plan has already
// worked out that the space runs out before them, and attempting them anyway
// would rediscover that one refusal at a time -- noisily, and after however
// long the titles before them took.
func splitPlan(plan app.InstallPlan) (todo []model.Game, notAttempted []string) {
	for _, p := range []app.PlatformPlan{plan.PS2, plan.PS1} {
		for _, e := range p.Entries {
			if e.Fits {
				todo = append(todo, e.Game)
				continue
			}
			notAttempted = append(notAttempted, e.Game.Title)
		}
	}
	return todo, notAttempted
}

// BatchReport is the outcome of a bulk install.
type BatchReport struct {
	Installed []model.Game `json:"installed,omitempty"`
	// Failed carries a message per title rather than one error for the run.
	// A batch that stopped at the first bad archive would leave the other five
	// hundred untouched for a reason that had nothing to do with them.
	Failed []BatchFailure `json:"failed,omitempty"`
	// NotAttempted are the titles the plan said would not fit. They are named
	// rather than tried, so the run does exactly what the plan promised.
	NotAttempted []string `json:"not_attempted,omitempty"`
	// Warnings are the per-install caveats, kept with the title they came from.
	Warnings  []string `json:"warnings,omitempty"`
	Cancelled bool     `json:"cancelled,omitempty"`
}

// BatchFailure is one title that could not be installed.
type BatchFailure struct {
	Title string `json:"title"`
	Err   string `json:"error"`
}

// runBatch installs everything in a plan that fits.
//
// Two things make this different from a loop over `install`. A failure stops
// that title and nothing else: across several hundred titles the odds of one
// bad archive approach certainty, and aborting the run for it would waste the
// hours already spent and every title after. And the titles the plan said
// would not fit are named rather than attempted, so the run does what the plan
// promised instead of discovering the same thing again one refusal at a time.
//
// There is no resume state to keep. Re-running skips what is already on the
// drive, which is the same answer a saved position would have given and cannot
// go stale.
func runBatch(env *Env, ctx context.Context, plan app.InstallPlan, opts app.InstallOptions) error {
	var rep BatchReport
	todo, notAttempted := splitPlan(plan)
	rep.NotAttempted = notAttempted
	if len(todo) == 0 {
		// renderPlan has already said why, so saying it again adds nothing.
		return nil
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
		r, err := env.Svc.Install(ctx, g, opts)
		switch {
		case errors.Is(err, context.Canceled):
			rep.Cancelled = true
		case errors.Is(err, app.ErrAlreadyInstalled):
			// Normal on a re-run, and not worth a line of its own.
		case err != nil:
			rep.Failed = append(rep.Failed, BatchFailure{Title: g.Title, Err: firstLine(err.Error())})
			env.warnf("  %s %s\n", amber("failed:"), firstLine(err.Error()))
		default:
			rep.Installed = append(rep.Installed, g)
			for _, w := range r.Warnings {
				rep.Warnings = append(rep.Warnings, g.Title+": "+w)
			}
		}
		if rep.Cancelled {
			break
		}
	}

	if env.JSON {
		return env.emitJSON(rep)
	}
	renderBatch(env, rep, len(todo))
	if len(rep.Failed) > 0 {
		return fmt.Errorf("%d of %d title(s) failed", len(rep.Failed), len(todo))
	}
	return nil
}

func renderBatch(env *Env, rep BatchReport, attempted int) {
	section(env.Out, "Done")
	pairs := [][2]string{
		{"Installed", fmt.Sprintf("%d of %d", len(rep.Installed), attempted)},
	}
	if len(rep.Failed) > 0 {
		pairs = append(pairs, [2]string{"Failed", amber(fmt.Sprintf("%d", len(rep.Failed)))})
	}
	if len(rep.NotAttempted) > 0 {
		pairs = append(pairs, [2]string{"No room for",
			dim(fmt.Sprintf("%d, not attempted", len(rep.NotAttempted)))})
	}
	if rep.Cancelled {
		pairs = append(pairs, [2]string{"Stopped", amber("interrupted")})
	}
	kv(env.Out, pairs)

	for _, f := range rep.Failed {
		env.printf("  %s %-40s %s\n", amber("!"), f.Title, dim(f.Err))
	}
	for _, w := range rep.Warnings {
		env.printf("  %s %s\n", amber("!"), w)
	}
	if rep.Cancelled || len(rep.Failed) > 0 {
		env.printf("\n%s\n", dim("Run the same command again to pick up where this left off; "+
			"titles already on the drive are skipped."))
	}
}
