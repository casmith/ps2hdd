package apa

import "testing"

const mib = int64(1024 * 1024)

// freeSlice builds a slice whose chunks are all free except those listed.
func freeSlice(total uint32, used ...uint32) Slice {
	s := Slice{TotalChunks: total, ChunkMap: make([]bool, total)}
	for _, u := range used {
		s.ChunkMap[u] = true
		s.UsedChunks++
	}
	s.FreeChunks = total - s.UsedChunks
	return s
}

// The overhead step is the whole point: 4 MiB for the main extent and 1 MiB
// per sub-extent are charged on top of the image, and an image that nearly
// fills its chunks is therefore charged another whole one. Rounding the image
// up to a chunk and stopping there -- which is what ps2hdd used to do -- gets
// these wrong by 128 MiB.
func TestAllocationChargesOverhead(t *testing.T) {
	// A large, entirely free slice: the buddy merge collapses the run, so the
	// overhead is only a few megabytes and only the last few MiB of a chunk
	// are affected.
	toc := &TOC{Slices: []Slice{freeSlice(512)}}
	cases := []struct {
		imageMB int64
		wantMB  int64
	}{
		{1, 128},
		{64, 128},
		// 124 MiB + 4 MiB of overhead is exactly one chunk, and the test in
		// apa.c is a strict "<", so this still fits.
		{124, 128},
		// 125 no longer does.
		{125, 256},
		{128, 256},
		{129, 256},
		{256, 384},
	}
	for _, tc := range cases {
		got, ok := toc.AllocationFor(tc.imageMB * mib)
		if !ok {
			t.Errorf("%d MiB: does not fit in a 64 GiB slice", tc.imageMB)
			continue
		}
		if got != tc.wantMB*mib {
			t.Errorf("%d MiB image: allocated %d MiB, want %d MiB", tc.imageMB, got/mib, tc.wantMB)
		}
	}
}

// The same image costs different amounts on different drives, because the
// overhead is charged per extent and fragmentation is what decides how many
// extents there are. This is why the prediction reads the chunk map instead of
// doing arithmetic on the size alone.
func TestAllocationDependsOnFragmentation(t *testing.T) {
	// Four chunks' worth with 6 MiB to spare: enough room for one extent's
	// 4 MiB of overhead, not enough for four extents' 7 MiB.
	const imageMB = 506
	contiguous := &TOC{Slices: []Slice{freeSlice(512)}}
	// Free chunks with a used one between each pair: nothing can merge.
	var used []uint32
	for i := uint32(1); i < 512; i += 2 {
		used = append(used, i)
	}
	fragmented := &TOC{Slices: []Slice{freeSlice(512, used...)}}

	a, ok := contiguous.AllocationFor(imageMB * mib)
	if !ok {
		t.Fatal("contiguous: does not fit")
	}
	b, ok := fragmented.AllocationFor(imageMB * mib)
	if !ok {
		t.Fatal("fragmented: does not fit")
	}
	if a != 512*mib {
		t.Errorf("contiguous: got %d MiB, want 512", a/mib)
	}
	// Four separate extents cost 3+4 = 7 MiB, and 506 + 7 passes 512.
	if b != 640*mib {
		t.Errorf("fragmented: got %d MiB, want 640", b/mib)
	}
	if b <= a {
		t.Error("fragmentation did not increase the cost")
	}
}

// optimize_partitions is a buddy merge, not a run-length merge: two extents
// join only when they are adjacent, the same size, and the pair is aligned to
// twice that size. A run therefore collapses towards its binary decomposition
// and no further.
func TestMergedExtentsIsABuddyMerge(t *testing.T) {
	run := func(start, n uint32) []uint32 {
		out := make([]uint32, n)
		for i := range out {
			out[i] = start + uint32(i)
		}
		return out
	}
	cases := []struct {
		name    string
		chunks  []uint32
		maxSize uint32
		want    uint32
	}{
		{"a single chunk", run(0, 1), 64, 1},
		{"an aligned power of two", run(0, 8), 64, 1},
		// 5 = 4 + 1 and the alignment allows both, so two extents.
		{"an aligned run of five", run(0, 5), 64, 2},
		// Starting at 1 spoils every pairing until the run reaches 2.
		{"a misaligned run of four", run(1, 4), 64, 3},
		// The cap stops the doubling even when alignment would allow it.
		{"capped at two chunks", run(0, 8), 2, 4},
		{"nothing adjacent", []uint32{0, 2, 4, 6}, 64, 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := mergedExtents(tc.chunks, tc.maxSize); got != tc.want {
				t.Errorf("got %d extents, want %d", got, tc.want)
			}
		})
	}
}

// A title that does not fit has to be reported as not fitting rather than
// silently returning a number, including when it is the overhead chunk that
// pushes it over the edge.
func TestAllocationReportsWhatDoesNotFit(t *testing.T) {
	toc := &TOC{Slices: []Slice{freeSlice(4)}}
	if _, ok := toc.AllocationFor(600 * mib); ok {
		t.Error("a 600 MiB image was said to fit in 512 MiB")
	}
	// Three chunks free, and an image needing three plus the overhead chunk.
	tight := &TOC{Slices: []Slice{freeSlice(4, 3)}}
	if _, ok := tight.AllocationFor(383 * mib); ok {
		t.Error("an image needing a fourth chunk fit in three")
	}
	if n, ok := tight.AllocationFor(300 * mib); !ok || n != 384*mib {
		t.Errorf("got %d MiB, %v; want 384 MiB, true", n/mib, ok)
	}
}

// The second slice of an APAEXT drive is a fallback, as apa_allocate_space
// tries each in turn.
func TestAllocationFallsThroughToTheSecondSlice(t *testing.T) {
	full := freeSlice(2, 0, 1)
	toc := &TOC{Slices: []Slice{full, freeSlice(64)}}
	got, ok := toc.AllocationFor(300 * mib)
	if !ok || got != 384*mib {
		t.Fatalf("got %d MiB, %v; want 384 MiB from the second slice", got/mib, ok)
	}
}

// MaxAllocationFor answers without a drive, so it has to assume the worst: no
// merging, a megabyte of overhead per chunk.
func TestMaxAllocationAssumesNoMerging(t *testing.T) {
	// 36 chunks at 1 MiB each plus 3 is 39 MiB of overhead, so anything above
	// 4569 MiB takes a 37th chunk.
	if got := MaxAllocationFor(4569 * mib); got != 4608*mib {
		t.Errorf("got %d MiB, want 4608", got/mib)
	}
	if got := MaxAllocationFor(4570 * mib); got != 4736*mib {
		t.Errorf("got %d MiB, want 4736", got/mib)
	}
	// It must never be cheaper than the best case a real drive could give.
	toc := &TOC{Slices: []Slice{freeSlice(1024)}}
	for _, mb := range []int64{1, 125, 700, 4480, 4600, 4700} {
		best, ok := toc.AllocationFor(mb * mib)
		if !ok {
			t.Fatalf("%d MiB does not fit", mb)
		}
		if MaxAllocationFor(mb*mib) < best {
			t.Errorf("%d MiB: worst case %d MiB is below the contiguous case %d MiB",
				mb, MaxAllocationFor(mb*mib)/mib, best/mib)
		}
	}
	if MaxAllocationFor(0) != 0 {
		t.Error("a zero-byte image was charged space")
	}
}
