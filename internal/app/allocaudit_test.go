package app

import (
	"strings"
	"testing"

	"github.com/casmith/ps2hdd/internal/apa"
)

const mib = int64(1024 * 1024)

// game builds a GameInfo the way the two readers do: the image size comes from
// the HDL header hdl_dump wrote, the footprint from the APA extent table. They
// are independent, which is what makes comparing them worth anything.
func game(name string, imageMB, footprintMB int64) apa.GameInfo {
	return apa.GameInfo{
		Name: name, Startup: "SLUS_209.46",
		RawSizeKB:   uint32(imageMB * 1024),
		AllocSizeKB: uint32(footprintMB * 1024),
	}
}

// A real allocation lies between covering the image with no overhead at all and
// charging a megabyte per chunk. Anything else means the arithmetic is wrong,
// and the arithmetic is what every space estimate and bulk plan is built on.
func TestAllocationCheckBrackets(t *testing.T) {
	// 3456 MiB needs 27 chunks (3456 MiB exactly), and with 27 chunks' worth
	// of overhead it needs 28.
	c := checkAllocation(game("Burnout 3", 3456, 3456))
	if c.MinBytes != 3456*mib {
		t.Errorf("min = %d MiB, want 3456", c.MinBytes/mib)
	}
	if c.MaxBytes != 3584*mib {
		t.Errorf("max = %d MiB, want 3584", c.MaxBytes/mib)
	}
	if !c.Within() {
		t.Error("an allocation at the minimum was called wrong")
	}
	if !checkAllocation(game("Burnout 3", 3456, 3584)).Within() {
		t.Error("an allocation at the maximum was called wrong")
	}
}

// The two ways the model can be wrong, and both must be caught. Less than the
// image needs is impossible; more than the worst case means the overhead rule
// is charging too little.
func TestAllocationCheckCatchesBothDirections(t *testing.T) {
	if checkAllocation(game("Too small", 3456, 3328)).Within() {
		t.Error("a partition smaller than its image was accepted")
	}
	if checkAllocation(game("Too large", 3456, 3712)).Within() {
		t.Error("a partition larger than the worst case was accepted")
	}
}

// An audit that could not read the drive must not read as agreement: an empty
// Outside then means "not looked at".
func TestAllocationAuditUncheckedIsNotOK(t *testing.T) {
	if (AllocationAudit{}).OK() {
		t.Error("an unchecked audit reported agreement")
	}
	if !(AllocationAudit{Checked: true, Total: 3}).OK() {
		t.Error("a checked audit with no disagreement did not report agreement")
	}
}

// The report has to carry the figures, because the only useful response to the
// model being wrong is someone reading the numbers it got wrong.
func TestAllocationAuditExplainCarriesTheFigures(t *testing.T) {
	a := AllocationAudit{
		Checked: true, Total: 2,
		Outside: []AllocationCheck{checkAllocation(game("Odd One", 3456, 4096))},
	}
	lines := a.Explain()
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want a summary and one title: %v", len(lines), lines)
	}
	for _, want := range []string{"Odd One", "3.4 GiB", "4.0 GiB"} {
		if !strings.Contains(strings.Join(lines, "\n"), want) {
			t.Errorf("the report does not mention %q:\n%s", want, strings.Join(lines, "\n"))
		}
	}
	if len((AllocationAudit{Checked: true}).Explain()) != 0 {
		t.Error("an audit with nothing wrong still explained something")
	}
}
