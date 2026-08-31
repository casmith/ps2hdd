package apa

// How much a PS2 title actually costs on the drive.
//
// It is not the image's size, and it is not the image rounded up to a chunk
// either. hdl_dump allocates the space (apa.c, apa_allocate_space_in_slice)
// and the arithmetic has a step that is easy to miss:
//
//  1. The image is rounded up to whole megabytes (hdl.c:1012).
//  2. Free 128 MiB chunks are taken, lowest first, until that many megabytes
//     are covered.
//  3. Adjacent chunks are merged pairwise into larger extents -- a buddy
//     merge: two runs join only when they are the same size and the pair is
//     aligned to twice it. What survives is one main partition and some number
//     of sub-partitions.
//  4. Overhead is then charged: 4 MiB for the main extent and 1 MiB for each
//     sub-extent. If the chunks taken in step 2 do not cover the image *plus*
//     that overhead, one more whole chunk is taken.
//
// Step 4 is the one that matters, and it makes the answer depend on where the
// free chunks are, not just how many there are: a contiguous run merges down
// to two or three extents and costs almost nothing in overhead, while a
// fragmented one merges to nothing and is charged 1 MiB per chunk. The same
// image can therefore cost 4608 MiB on one drive and 4736 MiB on another.
//
// So this does not estimate. It reads the real chunk map and replays the
// allocation.

// AllocationFor reports the bytes hdl_dump would allocate for an image of this
// size, and whether it would fit at all.
//
// It tries each slice in order, as apa_allocate_space does, and reports the
// first that can hold the title.
func (t *TOC) AllocationFor(imageBytes int64) (int64, bool) {
	for i := range t.Slices {
		if n, ok := t.Slices[i].allocationChunks(imageBytes); ok {
			return int64(n) * ChunkMB * 1024 * 1024, true
		}
	}
	return 0, false
}

// MaxAllocationFor reports what the title would cost with no contiguous free
// space at all: every chunk charged its own megabyte of overhead.
//
// This is the bound a pre-flight check wants. Refusing an install that would
// have fit by one chunk is a smaller harm than accepting one that runs out of
// room after copying four gigabytes, and unlike AllocationFor it needs no
// knowledge of the drive, so it is also what an unattached source listing can
// honestly show.
func MaxAllocationFor(imageBytes int64) int64 {
	n := chunksToCover(imageBytes)
	if n == 0 {
		return 0
	}
	// Worst case: nothing merges, so partitions used == chunks taken.
	if int64(ChunkMB)*int64(n) < megabytes(imageBytes)+overheadMB(n) {
		n++
	}
	return int64(n) * ChunkMB * 1024 * 1024
}

// megabytes rounds up to whole megabytes, as hdl.c:1012 does.
func megabytes(b int64) int64 {
	const mb = int64(1024 * 1024)
	if b <= 0 {
		return 0
	}
	return (b + mb - 1) / mb
}

// chunksToCover is how many chunks the fill loop takes before overhead.
func chunksToCover(imageBytes int64) uint32 {
	mb := megabytes(imageBytes)
	if mb <= 0 {
		return 0
	}
	return uint32((mb + ChunkMB - 1) / ChunkMB)
}

// overheadMB is 4 MiB for the main extent plus 1 MiB for each sub-extent,
// which apa.c spells as an accumulator starting at 3 and incremented once per
// extent.
func overheadMB(extents uint32) int64 { return 3 + int64(extents) }

// allocationChunks replays the allocation against one slice's real chunk map.
func (s *Slice) allocationChunks(imageBytes int64) (uint32, bool) {
	want := chunksToCover(imageBytes)
	if want == 0 {
		return 0, false
	}
	free := s.freeChunkIndices()
	if uint32(len(free)) < want {
		return 0, false
	}
	taken := free[:want]
	extents := mergedExtents(taken, s.maxExtentChunks())
	if int64(ChunkMB)*int64(want) < megabytes(imageBytes)+overheadMB(extents) {
		if uint32(len(free)) < want+1 {
			return 0, false
		}
		want++
	}
	return want, true
}

// freeChunkIndices lists the unoccupied chunks lowest first, which is the
// order apa.c fills them in.
func (s *Slice) freeChunkIndices() []uint32 {
	out := make([]uint32, 0, s.FreeChunks)
	for i, used := range s.ChunkMap {
		if !used {
			out = append(out, uint32(i))
		}
	}
	return out
}

// maxExtentChunks is the largest extent the merge is allowed to build:
// total_chunks/32 entries, floored at one, from apa.c.
func (s *Slice) maxExtentChunks() uint32 {
	if s.TotalChunks < 32 {
		return 1
	}
	return s.TotalChunks / 32
}

// mergedExtents counts what survives apa.c's optimize_partitions.
//
// Two neighbouring extents join only when they are adjacent, the same size,
// and the pair starts on a multiple of twice that size -- a buddy merge, so a
// run of chunks collapses towards its binary decomposition but only as far as
// its alignment allows. Sizes are counted in chunks here rather than the
// megabytes apa.c uses; the two differ by a constant factor and the alignment
// test is unaffected.
func mergedExtents(chunks []uint32, maxExtent uint32) uint32 {
	if len(chunks) == 0 {
		return 0
	}
	type extent struct{ start, size uint32 }
	ex := make([]extent, len(chunks))
	for i, c := range chunks {
		ex[i] = extent{start: c, size: 1}
	}
	for joined := true; joined; {
		joined = false
		for i := 0; i+1 < len(ex); i++ {
			pair := ex[i].size * 2
			if pair <= maxExtent &&
				ex[i].start+ex[i].size == ex[i+1].start &&
				ex[i].size == ex[i+1].size &&
				ex[i].start%pair == 0 {
				ex[i].size = pair
				ex = append(ex[:i+1], ex[i+2:]...)
				joined = true
			}
		}
	}
	return uint32(len(ex))
}
