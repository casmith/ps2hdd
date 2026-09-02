package app

import (
	"context"
	"fmt"
	"os"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/model"
)

// Checking the allocation model against the drive that disproves it.
//
// What a PS2 title costs is worked out here by replaying hdl_dump's allocator,
// which was written from its C and never run against it: hdl_dump only accepts
// block devices, so a file-backed cross-check needs root and a loop device.
// Every fit verdict and every bulk plan rests on arithmetic that had been read
// and not tested.
//
// An installed drive is the test. Each HDL partition records two independent
// numbers: the image's size, written into the HDL header by hdl_dump, and the
// space the partition occupies, which comes from the APA extent table. The
// second is what hdl_dump decided when it allocated, and the first is what it
// decided from. So the model can be held against real allocations it did not
// produce.
//
// It is checked as a range rather than an exact figure, and that is not a
// hedge. The overhead hdl_dump charges depends on how many extents the
// partition ended up as, which depends on where the free chunks were at the
// time -- a state the drive no longer remembers. What does not depend on it is
// that the answer lies between covering the image with no overhead at all and
// charging a megabyte per chunk. An allocation outside that says the model is
// wrong, not imprecise.

// AllocationCheck is one installed title measured against the model.
type AllocationCheck struct {
	Title  string `json:"title"`
	GameID string `json:"game_id"`
	// ImageBytes is what hdl_dump recorded the source image as.
	ImageBytes int64 `json:"image_bytes"`
	// ActualBytes is what the partition and its extents occupy.
	ActualBytes int64 `json:"actual_bytes"`
	// MinBytes and MaxBytes bracket what the model says it should be.
	MinBytes int64 `json:"min_bytes"`
	MaxBytes int64 `json:"max_bytes"`
}

// Within reports whether the real allocation agrees with the model.
func (c AllocationCheck) Within() bool {
	return c.ActualBytes >= c.MinBytes && c.ActualBytes <= c.MaxBytes
}

// AllocationAudit is the model held against every installed title.
type AllocationAudit struct {
	// Checked is false when the drive could not be read, in which case an
	// empty Outside means "not looked at", not "all correct".
	Checked bool              `json:"checked"`
	Total   int               `json:"total"`
	Outside []AllocationCheck `json:"outside,omitempty"`
	Skipped int               `json:"skipped,omitempty"`
}

// OK reports whether every title agrees with the model.
func (a AllocationAudit) OK() bool { return a.Checked && len(a.Outside) == 0 }

// Explain describes any disagreement.
func (a AllocationAudit) Explain() []string {
	if len(a.Outside) == 0 {
		return nil
	}
	out := []string{fmt.Sprintf(
		"%d of %d installed title(s) occupy space the allocation model does not predict. "+
			"Space estimates and `install --all` plans are computed from that model, so they "+
			"are wrong by at least as much. Please report this with the figures below.",
		len(a.Outside), a.Total)}
	for _, c := range a.Outside {
		out = append(out, fmt.Sprintf("  %s (%s): image %s, occupies %s, model says %s to %s",
			c.Title, c.GameID,
			model.HumanSize(c.ImageBytes), model.HumanSize(c.ActualBytes),
			model.HumanSize(c.MinBytes), model.HumanSize(c.MaxBytes)))
	}
	return out
}

// checkAllocation brackets one title's real footprint with what the model says
// it should be.
func checkAllocation(g apa.GameInfo) AllocationCheck {
	image := g.ImageBytes()
	return AllocationCheck{
		Title: g.Name, GameID: model.OPLGameID(g.Startup),
		ImageBytes: image, ActualBytes: g.SizeBytes(),
		MinBytes: apa.MinAllocationFor(image),
		MaxBytes: apa.MaxAllocationFor(image),
	}
}

// AuditAllocations measures every installed PS2 title against the model.
func (s *Services) AuditAllocations(ctx context.Context) (AllocationAudit, error) {
	var a AllocationAudit
	t, err := s.Target(ctx, false)
	if err != nil {
		return a, err
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return a, err
	}
	defer f.Close()
	toc, err := apa.ReadTOC(f, t.SizeBytes)
	if err != nil {
		return a, err
	}
	games, err := apa.ReadGames(f, toc)
	if err != nil {
		return a, err
	}
	a.Checked = true
	for _, g := range games {
		image, actual := g.ImageBytes(), g.SizeBytes()
		// A header that records no image size says nothing either way.
		if image <= 0 || actual <= 0 {
			a.Skipped++
			continue
		}
		a.Total++
		c := checkAllocation(g)
		if !c.Within() {
			a.Outside = append(a.Outside, c)
		}
	}
	return a, nil
}
