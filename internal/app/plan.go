package app

import (
	"context"
	"os"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
)

// PlanEntry is one title in a bulk plan.
type PlanEntry struct {
	Game model.Game `json:"game"`
	// Bytes is what this title costs where it lands: an APA allocation for a
	// PS2 title, a VCD inside __.POPS for a PS1 one.
	Bytes int64 `json:"bytes"`
	// Fits is false once the space it needs has already been spoken for.
	Fits bool `json:"fits"`
}

// PlatformPlan is the half of a plan belonging to one console.
type PlatformPlan struct {
	Platform model.Platform `json:"platform"`
	// Where names the space these titles consume, for a reader who has to act
	// on the answer: unallocated APA chunks and room inside __.POPS are
	// different things and are fixed in different ways.
	Where string `json:"where"`
	// FreeBytes is what was available before any of this.
	FreeBytes int64 `json:"free_bytes"`
	// FreeKnown is false when the space could not be measured, in which case
	// every title is reported as fitting because nothing said otherwise.
	FreeKnown bool        `json:"free_known"`
	Entries   []PlanEntry `json:"entries,omitempty"`
}

// Count is how many titles the plan covers.
func (p PlatformPlan) Count() int { return len(p.Entries) }

// Bytes totals what the plan would consume.
func (p PlatformPlan) Bytes() int64 {
	var n int64
	for _, e := range p.Entries {
		n += e.Bytes
	}
	return n
}

// Fitting and Excess split the plan at the point the space runs out.
func (p PlatformPlan) Fitting() (count int, bytes int64) {
	for _, e := range p.Entries {
		if e.Fits {
			count++
			bytes += e.Bytes
		}
	}
	return count, bytes
}

// Excess reports the titles that will not fit and what they would need.
func (p PlatformPlan) Excess() (count int, bytes int64) {
	for _, e := range p.Entries {
		if !e.Fits {
			count++
			bytes += e.Bytes
		}
	}
	return count, bytes
}

// InstallPlan is what installing a set of titles would cost.
type InstallPlan struct {
	PS2 PlatformPlan `json:"ps2"`
	PS1 PlatformPlan `json:"ps1"`
	// Skipped counts source titles left out because they are already on the
	// drive. Reporting the number rather than the titles keeps a plan for a
	// mostly-installed library readable.
	Skipped int `json:"skipped"`
}

// Fits reports whether the whole plan would go on as it stands.
func (p InstallPlan) Fits() bool {
	n2, _ := p.PS2.Excess()
	n1, _ := p.PS1.Excess()
	return n2 == 0 && n1 == 0
}

// PlanOptions selects what a plan covers.
type PlanOptions struct {
	// PS2 and PS1 include that platform. Both false means both.
	PS2, PS1 bool
}

func (o PlanOptions) wants(p model.Platform) bool {
	if !o.PS2 && !o.PS1 {
		return true
	}
	return (p == model.PlatformPS2 && o.PS2) || (p == model.PlatformPS1 && o.PS1)
}

// PlanInstallAll works out what installing everything available in the source
// directories would cost, without writing anything.
//
// Space is consumed as the plan walks the list, which is the whole point.
// Checking each title against the drive's current free space would report that
// every one of five hundred titles fits, because individually every one of
// them does. The question a bulk plan answers is where the run stops, and that
// needs each title placed on a drive the earlier ones have already filled.
//
// PS2 titles are placed by replaying hdl_dump's allocator against a working
// copy of the real chunk map, so fragmentation and partition overhead are part
// of the answer rather than an approximation of it. PS1 titles are VCDs inside
// __.POPS, an already-allocated partition, so they are counted against the
// room left in it instead.
func (s *Services) PlanInstallAll(ctx context.Context, opts PlanOptions) (InstallPlan, []error, error) {
	var plan InstallPlan
	c, warnings, err := s.Catalog(ctx)
	if err != nil {
		return plan, warnings, err
	}

	var ps2, ps1Games []model.Game
	for _, e := range c.Entries {
		if !e.AvailableInSource {
			continue
		}
		if e.Installed {
			if opts.wants(e.Platform) {
				plan.Skipped++
			}
			continue
		}
		if !opts.wants(e.Platform) {
			continue
		}
		g := e.Game
		if e.SourceGame != nil {
			g = *e.SourceGame
		}
		if g.Platform == model.PlatformPS2 {
			ps2 = append(ps2, g)
		} else {
			ps1Games = append(ps1Games, g)
		}
	}

	sel := append(ps2, ps1Games...)
	planned, err := s.PlanInstall(ctx, sel, opts)
	if err != nil {
		return plan, warnings, err
	}
	planned.Skipped = plan.Skipped
	return planned, warnings, nil
}

// PlanInstall works out what installing a given set of titles would cost.
//
// The set is whatever the caller has already chosen -- everything available,
// or the lines of a list file -- and the accounting is the same either way:
// each title is placed on a drive the ones before it have filled.
func (s *Services) PlanInstall(ctx context.Context, games []model.Game, opts PlanOptions) (InstallPlan, error) {
	var plan InstallPlan
	var ps2, ps1Games []model.Game
	for _, g := range games {
		if !opts.wants(g.Platform) {
			continue
		}
		if g.Platform == model.PlatformPS2 {
			ps2 = append(ps2, g)
		} else {
			ps1Games = append(ps1Games, g)
		}
	}
	var err error
	if opts.wants(model.PlatformPS2) {
		plan.PS2, err = s.planPS2(ctx, ps2)
		if err != nil {
			return plan, err
		}
	}
	if opts.wants(model.PlatformPS1) {
		plan.PS1, err = s.planPS1(ctx, ps1Games)
		if err != nil {
			return plan, err
		}
	}
	return plan, nil
}

// planPS2 places each image against a working copy of the APA chunk map.
func (s *Services) planPS2(ctx context.Context, games []model.Game) (PlatformPlan, error) {
	p := PlatformPlan{Platform: model.PlatformPS2, Where: "the APA partition table"}
	model.SortGames(games)

	alloc, err := s.allocator(ctx)
	if err != nil {
		// No drive, or one that could not be read: the sizes are still worth
		// having, and saying "fits" when nothing was measured would be a
		// claim rather than an answer. FreeKnown records which it is.
		for _, g := range games {
			p.Entries = append(p.Entries, PlanEntry{
				Game: g, Bytes: apa.MaxAllocationFor(g.SizeBytes), Fits: true,
			})
		}
		return p, nil
	}
	p.FreeKnown = true
	p.FreeBytes = alloc.FreeBytes()
	for _, g := range games {
		n, ok := alloc.Place(g.SizeBytes)
		if !ok {
			n = apa.MaxAllocationFor(g.SizeBytes)
		}
		p.Entries = append(p.Entries, PlanEntry{Game: g, Bytes: n, Fits: ok})
	}
	return p, nil
}

// allocator opens the drive and takes a working copy of its free space.
func (s *Services) allocator(ctx context.Context) (*apa.Allocator, error) {
	t, err := s.Target(ctx, false)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, t.SizeBytes)
	if err != nil {
		return nil, err
	}
	return toc.NewAllocator(), nil
}

// planPS1 counts VCDs against the room left inside __.POPS.
func (s *Services) planPS1(ctx context.Context, games []model.Game) (PlatformPlan, error) {
	p := PlatformPlan{Platform: model.PlatformPS1, Where: ps1.POPSPartition}
	model.SortGames(games)

	free, known := s.popsFree(ctx)
	p.FreeKnown = known
	p.FreeBytes = free
	p.Entries = fitSequentially(games, free, known)
	return p, nil
}

// fitSequentially places each title against what the ones before it left.
//
// A VCD is a file in an already-allocated partition, so unlike a PS2 title
// there is no chunk arithmetic -- but the running total matters just as much.
// Checking every title against the partition's full free space would report a
// library twice the size of __.POPS as fitting, title by title.
//
// known false means nothing measured the space. Everything is then reported as
// fitting, because "nothing said otherwise" is the honest answer and a verdict
// drawn from a free figure of zero would be a fiction.
func fitSequentially(games []model.Game, free int64, known bool) []PlanEntry {
	out := make([]PlanEntry, 0, len(games))
	remaining := free
	for _, g := range games {
		n := g.InstallSize()
		fits := true
		if known {
			fits = n <= remaining
			if fits {
				remaining -= n
			}
		}
		out = append(out, PlanEntry{Game: g, Bytes: n, Fits: fits})
	}
	return out
}

// popsFree measures the room left in __.POPS. known is false when there is no
// drive, no __.POPS, or no way to mount it -- all of which are ordinary states
// for a plan run before the partition exists.
func (s *Services) popsFree(ctx context.Context) (int64, bool) {
	m, err := s.Mounts(ctx)
	if err != nil {
		return 0, false
	}
	var free int64
	if err := m.With(ctx, ps1.POPSPartition, func(mp string) error {
		free, err = freeSpace(mp)
		return err
	}); err != nil {
		return 0, false
	}
	if free <= 0 {
		return 0, false
	}
	return free, true
}
