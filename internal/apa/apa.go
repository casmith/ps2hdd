// Package apa reads the PlayStation 2 APA partition table and the HDLoader
// game headers stored inside it.
//
// This package is strictly read-only. ps2hdd does not reimplement APA
// allocation, partition creation or deletion: every write goes through
// hdl_dump, which is the reference implementation. What is implemented here is
// parsing, because the read path has to work for `detect`, `status` and `list`
// even on a machine where hdl_dump is not installed, and because parsing a
// 1 KiB struct is far easier to test than shelling out.
//
// Layout facts below were taken from ps2homebrew/hdl-dump: ps2_hdd.h for the
// on-disk partition header, apa.c for the slice walk and the chunk accounting,
// and hdl.c for the HDL game header at partition offset 0x101000.
package apa

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strings"
)

// SectorSize is the APA sector size. Everything on a PS2 HDD is addressed in
// 512-byte sectors regardless of the drive's physical sector size.
const SectorSize = 512

// HeaderSize is the size of one on-disk partition header.
const HeaderSize = 1024

// ChunkMB is the APA allocation granularity: partitions always start on and
// span whole 128 MiB chunks.
const ChunkMB = 128

// chunkSectors is 128 MiB expressed in 512-byte sectors.
const chunkSectors = ChunkMB * 1024 * 1024 / SectorSize

// slice2Offset is the sector offset of the second APA slice on drives larger
// than 128 GiB that were extended by ToxicOS/APAEXT.
const slice2Offset = 0x10000000

// Partition types seen on a PS2 HDD.
const (
	TypeMBR   uint16 = 0x0001
	TypeSwap  uint16 = 0x0082
	TypeLinux uint16 = 0x0083
	TypePFS   uint16 = 0x0100 // "game" partition; PFS filesystems live here
	TypeHDL   uint16 = 0x1337 // HDLoader game image
)

// EmptyPartitionID is the name APA gives a partition that has been removed.
//
// Removal does not unlink anything. apaRemovePartition rewrites the header in
// place -- memset to zero, then magic, start, next, prev, length and the id
// "__empty" (ps2sdk, iop/hdd/libapa/src/apa.c) -- so the entry stays in the
// chain, keeps its extent, and means "this space is free".
//
// Anything that walks the chain and counts every entry as occupied therefore
// reports a drive that never gets emptier, however much is removed from it.
// hdl_dump drops these entries as it reads a slice and returns their chunks to
// the free map (apa.c, AUTO_DELETE_EMPTY); this package does the same.
const EmptyPartitionID = "__empty"

// IsEmpty reports whether this header describes freed space rather than a
// partition.
func (h Header) IsEmpty() bool {
	return strings.EqualFold(h.ID, EmptyPartitionID)
}

// flagSub marks a header as belonging to a sub-partition.
const flagSub uint16 = 0x0001

var (
	// ErrNotAPA means the first sector did not hold a valid APA header. The
	// caller must treat the disk as unknown and refuse to write to it.
	ErrNotAPA = errors.New("device is not APA partitioned")
	// ErrBadAPA means the table is structurally damaged. ps2hdd never repairs
	// a damaged table; it reports and refuses.
	ErrBadAPA = errors.New("APA partition table is damaged")
)

// magic is the APA signature, "APA\0".
var magic = [4]byte{'A', 'P', 'A', 0}

// Header is one decoded APA partition header.
type Header struct {
	Checksum uint32
	Next     uint32 // sector of the next partition, 0 terminates the list
	Prev     uint32
	ID       string // "__mbr", "+OPL", "PP.HDL.Game", "__.POPS"
	Start    uint32 // sector
	Length   uint32 // sectors
	Type     uint16
	Flags    uint16
	NSub     uint32
	Main     uint32 // for sub-partitions: sector of the main partition
	Number   uint32
	Name     string
	Subs     []SubPartition

	// ToxicMagic and ToxicFlags only carry meaning in the MBR header.
	ToxicMagic string
	ToxicFlags uint32
}

// SubPartition is one extent belonging to a main partition.
type SubPartition struct {
	Start  uint32
	Length uint32
}

// IsMain reports whether this header describes a main partition rather than
// one of its extents.
func (h Header) IsMain() bool { return h.Main == 0 && h.Flags&flagSub == 0 }

// TotalSectors is the main extent plus every sub-extent.
func (h Header) TotalSectors() uint64 {
	total := uint64(h.Length)
	for _, s := range h.Subs {
		total += uint64(s.Length)
	}
	return total
}

// Slice is one APA slice: the whole table on drives up to 128 GiB, or one half
// of an APAEXT-extended table on larger ones.
type Slice struct {
	Index      int
	SizeMB     uint32
	Partitions []Header

	TotalChunks uint32
	UsedChunks  uint32
	FreeChunks  uint32
	// ChunkMap marks which of the slice's 128 MiB chunks are occupied. It is
	// what makes an allocation predictable rather than estimated: how much a
	// title costs depends on where the free chunks are, not only how many
	// there are. See AllocationFor.
	ChunkMap []bool
}

// TOC is the full partition table of a device.
type TOC struct {
	SizeKB uint32

	IsToxic     bool
	Is2Slice    bool
	Got2ndSlice bool

	Slices []Slice
}

// Partitions returns every main partition across all slices.
func (t *TOC) Partitions() []Header {
	var out []Header
	for _, s := range t.Slices {
		for _, p := range s.Partitions {
			if p.IsMain() && !p.IsEmpty() {
				out = append(out, p)
			}
		}
	}
	return out
}

// Find returns the main partition with the given id, ignoring case and
// trailing padding, as hdl_dump does.
func (t *TOC) Find(id string) (Header, int, bool) {
	for _, s := range t.Slices {
		for _, p := range s.Partitions {
			if p.IsMain() && !p.IsEmpty() && strings.EqualFold(p.ID, id) {
				return p, s.Index, true
			}
		}
	}
	return Header{}, 0, false
}

// Chunks totals the allocation figures across slices.
func (t *TOC) Chunks() (total, used, free uint32) {
	for _, s := range t.Slices {
		total += s.TotalChunks
		used += s.UsedChunks
		free += s.FreeChunks
	}
	return
}

// checksum sums words 1..255 of the raw header, which is how the stored
// checksum at word 0 is computed.
func checksum(raw []byte) uint32 {
	var sum uint32
	for i := 1; i < HeaderSize/4; i++ {
		sum += binary.LittleEndian.Uint32(raw[i*4:])
	}
	return sum
}

// decodeHeader parses a raw 1024-byte header. It does not validate.
func decodeHeader(raw []byte) Header {
	h := Header{
		Checksum: binary.LittleEndian.Uint32(raw[0:]),
		Next:     binary.LittleEndian.Uint32(raw[8:]),
		Prev:     binary.LittleEndian.Uint32(raw[12:]),
		ID:       trimField(raw[16:48]),
		Start:    binary.LittleEndian.Uint32(raw[64:]),
		Length:   binary.LittleEndian.Uint32(raw[68:]),
		Type:     binary.LittleEndian.Uint16(raw[72:]),
		Flags:    binary.LittleEndian.Uint16(raw[74:]),
		NSub:     binary.LittleEndian.Uint32(raw[76:]),
		Main:     binary.LittleEndian.Uint32(raw[88:]),
		Number:   binary.LittleEndian.Uint32(raw[92:]),
		Name:     trimField(raw[128:256]),

		ToxicMagic: string(raw[500:508]),
		ToxicFlags: binary.LittleEndian.Uint32(raw[508:]),
	}
	// nsub is bounded by the 64 sub-extent slots in the on-disk struct; a
	// larger value means a corrupt header, and clamping keeps the decode
	// total rather than panicking on a slice bound.
	n := int(h.NSub)
	if n > 64 {
		n = 64
	}
	for i := 0; i < n; i++ {
		off := 512 + i*8
		h.Subs = append(h.Subs, SubPartition{
			Start:  binary.LittleEndian.Uint32(raw[off:]),
			Length: binary.LittleEndian.Uint32(raw[off+4:]),
		})
	}
	return h
}

// trimField turns a fixed-width, NUL- or space-padded on-disk string field
// into a Go string.
func trimField(b []byte) string {
	if i := indexByte(b, 0); i >= 0 {
		b = b[:i]
	}
	return strings.TrimRight(string(b), " ")
}

func indexByte(b []byte, c byte) int {
	for i := range b {
		if b[i] == c {
			return i
		}
	}
	return -1
}

// hasMagic reports whether raw starts with the APA signature.
func hasMagic(raw []byte) bool {
	return raw[4] == magic[0] && raw[5] == magic[1] && raw[6] == magic[2] && raw[7] == magic[3]
}

// IsAPA reports whether the device's first sector carries a valid APA header.
// A checksum mismatch counts as "not APA": a disk that merely looks APA-ish is
// exactly the case where guessing is dangerous.
func IsAPA(r io.ReaderAt) (bool, error) {
	raw := make([]byte, HeaderSize)
	if _, err := r.ReadAt(raw, 0); err != nil {
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			return false, nil
		}
		return false, err
	}
	return hasMagic(raw) && binary.LittleEndian.Uint32(raw) == checksum(raw), nil
}

// ReadTOC walks the partition list of a device whose total size is sizeBytes.
//
// The walk follows the `next` sector chain, exactly as hdl_dump does, and
// stops on a header whose magic or checksum does not verify. Every partition
// must lie wholly inside the slice; one that does not means the table has been
// written by a tool that disagrees with this one about geometry, and the read
// fails rather than reporting a plausible-looking table.
func ReadTOC(r io.ReaderAt, sizeBytes int64) (*TOC, error) {
	if sizeBytes <= 0 {
		return nil, fmt.Errorf("device size is unknown")
	}
	raw := make([]byte, HeaderSize)
	if _, err := readFull(r, raw, 0); err != nil {
		return nil, fmt.Errorf("read APA master boot record: %w", err)
	}
	if !hasMagic(raw) {
		return nil, ErrNotAPA
	}
	if binary.LittleEndian.Uint32(raw) != checksum(raw) {
		return nil, fmt.Errorf("%w: master boot record checksum mismatch", ErrNotAPA)
	}
	mbr := decodeHeader(raw)

	sizeKB := uint32(sizeBytes / 1024)
	toc := &TOC{SizeKB: sizeKB}
	toc.IsToxic = mbr.ToxicMagic == "APAEXT\x00\x00"
	if toc.IsToxic {
		toc.Is2Slice = mbr.ToxicFlags&0x01 != 0
		// The second slice only exists once the drive is actually bigger than
		// the 128 GiB mark the extension works around.
		toc.Got2ndSlice = toc.Is2Slice && sizeKB > 128*1024*1024
	}

	count := 1
	if toc.Got2ndSlice {
		count = 2
	}
	for i := 0; i < count; i++ {
		s, err := readSlice(r, toc, i)
		if err != nil {
			return nil, err
		}
		toc.Slices = append(toc.Slices, s)
	}
	return toc, nil
}

func readSlice(r io.ReaderAt, toc *TOC, index int) (Slice, error) {
	const exactly128MB = 128 * 1024 * 1024 // KB
	const almost128MB = exactly128MB - 1

	var totalSectors uint32
	switch {
	case !toc.Got2ndSlice:
		totalSectors = toc.SizeKB * 2
	case index == 0:
		kb := toc.SizeKB
		if kb > almost128MB {
			kb = almost128MB
		}
		totalSectors = kb * 2
	default:
		totalSectors = (toc.SizeKB - exactly128MB) * 2
	}

	s := Slice{Index: index, SizeMB: totalSectors / 2048}
	base := int64(index) * slice2Offset * SectorSize

	raw := make([]byte, HeaderSize)
	seen := map[uint32]bool{}
	sector := uint32(0)
	for {
		if seen[sector] {
			return s, fmt.Errorf("%w: partition list loops back to sector %d", ErrBadAPA, sector)
		}
		seen[sector] = true

		off := base + int64(sector)*SectorSize
		if _, err := readFull(r, raw, off); err != nil {
			return s, fmt.Errorf("read partition header at sector %d: %w", sector, err)
		}
		if !hasMagic(raw) {
			return s, fmt.Errorf("%w: no APA signature at sector %d", ErrNotAPA, sector)
		}
		if binary.LittleEndian.Uint32(raw) != checksum(raw) {
			return s, fmt.Errorf("%w: checksum mismatch at sector %d", ErrBadAPA, sector)
		}
		h := decodeHeader(raw)
		if uint64(h.Start)+uint64(h.Length) > uint64(totalSectors) {
			return s, fmt.Errorf("%w: partition %q extends past the end of slice %d", ErrBadAPA, h.ID, index)
		}
		s.Partitions = append(s.Partitions, h)
		if len(s.Partitions) > 10000 {
			return s, fmt.Errorf("%w: implausible partition count", ErrBadAPA)
		}
		if h.Next == 0 {
			break
		}
		sector = h.Next
	}
	if len(s.Partitions) == 0 {
		return s, fmt.Errorf("%w: no partitions found", ErrBadAPA)
	}
	setupStatistics(&s)
	return s, nil
}

// setupStatistics reproduces hdl_dump's chunk accounting: every partition
// occupies a whole number of 128 MiB chunks starting at its own chunk index.
func setupStatistics(s *Slice) {
	s.TotalChunks = s.SizeMB / ChunkMB
	s.FreeChunks = s.TotalChunks
	s.UsedChunks = 0
	occupied := make([]bool, s.TotalChunks)
	mark := func(start, length uint32) {
		first := start / chunkSectors
		n := length / chunkSectors
		for i := uint32(0); i < n; i++ {
			c := first + i
			if c >= uint32(len(occupied)) {
				continue
			}
			if !occupied[c] {
				occupied[c] = true
				s.UsedChunks++
				if s.FreeChunks > 0 {
					s.FreeChunks--
				}
			}
		}
	}
	for _, p := range s.Partitions {
		// A removed partition is still in the chain, named __empty, and its
		// chunks are free. Counting them is what makes a removal look like it
		// reclaimed nothing.
		if p.IsEmpty() {
			continue
		}
		mark(p.Start, p.Length)
	}
	s.ChunkMap = occupied
}

// readFull reads exactly len(buf) bytes at off.
func readFull(r io.ReaderAt, buf []byte, off int64) (int, error) {
	n, err := r.ReadAt(buf, off)
	if err != nil && n == len(buf) {
		err = nil
	}
	if err != nil {
		return n, err
	}
	return n, nil
}
