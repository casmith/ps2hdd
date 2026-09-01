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

// A plan is not a stack of independent answers. Asking whether each of three
// titles fits on the same empty drive says yes three times; asking where the
// run stops needs the space claimed as it goes.
func TestAllocatorConsumesSpaceAsItGoes(t *testing.T) {
	toc := &TOC{Slices: []Slice{freeSlice(8)}} // 1 GiB
	a := toc.NewAllocator()

	// Three 300 MiB images: 384 MiB each once rounded, so two fit and the
	// third does not.
	for i, want := range []struct {
		bytes int64
		ok    bool
	}{{384, true}, {384, true}, {0, false}} {
		got, ok := a.Place(300 * mib)
		if ok != want.ok {
			t.Fatalf("title %d: fits = %v, want %v", i+1, ok, want.ok)
		}
		if ok && got != want.bytes*mib {
			t.Errorf("title %d: allocated %d MiB, want %d", i+1, got/mib, want.bytes)
		}
	}
	// Two of eight chunks are left, which is not enough for a third.
	if got, want := a.FreeBytes(), int64(2*128)*mib; got != want {
		t.Errorf("free = %d MiB, want %d", got/mib, want/mib)
	}
}

// A refused placement must claim nothing, or one oversized title would eat the
// space the rest of the plan was going to use.
func TestAllocatorClaimsNothingWhenItRefuses(t *testing.T) {
	toc := &TOC{Slices: []Slice{freeSlice(4)}} // 512 MiB
	a := toc.NewAllocator()
	before := a.FreeBytes()
	if _, ok := a.Place(4000 * mib); ok {
		t.Fatal("a 4 GiB image fit in 512 MiB")
	}
	if got := a.FreeBytes(); got != before {
		t.Errorf("free went from %d to %d MiB on a refusal", before/mib, got/mib)
	}
	// And the space is still there for something that does fit.
	if _, ok := a.Place(300 * mib); !ok {
		t.Error("the refusal consumed the space anyway")
	}
}

// The allocator works on a copy. A plan that quietly edited the table it read
// would make the next question in the same session answer wrongly.
func TestAllocatorDoesNotDisturbTheTable(t *testing.T) {
	toc := &TOC{Slices: []Slice{freeSlice(8)}}
	a := toc.NewAllocator()
	for i := 0; i < 4; i++ {
		a.Place(200 * mib)
	}
	if toc.Slices[0].FreeChunks != 8 {
		t.Errorf("the source table lost chunks: free = %d, want 8", toc.Slices[0].FreeChunks)
	}
	for i, used := range toc.Slices[0].ChunkMap {
		if used {
			t.Fatalf("the source chunk map was written to at %d", i)
		}
	}
}

// Placement keeps charging real overhead as it goes, so a run of titles that
// each sit just under a chunk boundary costs a chunk each more than their
// sizes suggest.
func TestAllocatorKeepsChargingOverhead(t *testing.T) {
	toc := &TOC{Slices: []Slice{freeSlice(64)}}
	a := toc.NewAllocator()
	for i := 0; i < 3; i++ {
		got, ok := a.Place(125 * mib)
		if !ok {
			t.Fatalf("title %d did not fit", i+1)
		}
		if got != 256*mib {
			t.Errorf("title %d allocated %d MiB, want 256", i+1, got/mib)
		}
	}
}

// APA does not unlink a removed partition. apaRemovePartition rewrites its
// header in place as "__empty", so the entry stays in the chain with the same
// extent and means "this space is free" -- which is why hdl_dump drops those
// entries and returns their chunks to the free map as it reads a slice.
//
// Counting them as occupied is a drive that never gets emptier: remove
// thirty-five games and the free space does not move.
func TestEmptyPartitionsAreFreeSpace(t *testing.T) {
	full := freeSlice(64, 0, 1, 2, 3)
	full.Partitions = []Header{
		{ID: "__mbr", Start: 0, Length: chunkSectors},
		{ID: "PP.SLUS_210.50.Burnout 3", Start: chunkSectors, Length: chunkSectors},
		// Two chunks that were a game until it was removed.
		{ID: EmptyPartitionID, Start: 2 * chunkSectors, Length: 2 * chunkSectors},
	}
	full.SizeMB = 64 * ChunkMB
	setupStatistics(&full)

	// Four chunks are described, but only two of them are in use.
	if full.UsedChunks != 2 {
		t.Errorf("used = %d, want 2: the __empty entry is free space", full.UsedChunks)
	}
	if want := full.TotalChunks - 2; full.FreeChunks != want {
		t.Errorf("free = %d, want %d", full.FreeChunks, want)
	}
	// And the allocator can place a title in the space that was given back.
	for i, used := range full.ChunkMap {
		if i >= 2 && i < 4 && used {
			t.Errorf("chunk %d is still marked used after the partition was removed", i)
		}
	}
}

// An emptied partition is not a partition: it must not be listed as one, and
// must not answer to the name of the game that used to be there.
func TestEmptyPartitionsAreNotListed(t *testing.T) {
	s := freeSlice(8)
	s.SizeMB = 8 * ChunkMB
	s.Partitions = []Header{
		{ID: "__mbr", Start: 0, Length: chunkSectors},
		{ID: EmptyPartitionID, Start: chunkSectors, Length: chunkSectors},
	}
	toc := &TOC{Slices: []Slice{s}}
	for _, p := range toc.Partitions() {
		if p.IsEmpty() {
			t.Errorf("%q was listed as a partition", p.ID)
		}
	}
	if _, _, ok := toc.Find(EmptyPartitionID); ok {
		t.Error("an emptied partition answered to its name")
	}
}

// The name is how both the driver and hdl_dump identify these, so the check is
// on the name and is not case sensitive.
func TestIsEmpty(t *testing.T) {
	for _, id := range []string{"__empty", "__EMPTY", "__Empty"} {
		if !(Header{ID: id}).IsEmpty() {
			t.Errorf("%q was not recognised as empty", id)
		}
	}
	for _, id := range []string{"__mbr", "+OPL", "__.POPS", "PP.SLUS_210.50.Burnout 3", ""} {
		if (Header{ID: id}).IsEmpty() {
			t.Errorf("%q was treated as empty", id)
		}
	}
}
