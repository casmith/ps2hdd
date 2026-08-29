package apa

import (
	"encoding/binary"
	"fmt"
	"io"
	"strings"
)

// hdlHeaderOffset is where hdl_dump writes the HDLoader game descriptor inside
// a type-0x1337 partition, one megabyte in (hdl.c: HDL_HEADER_OFFS).
const hdlHeaderOffset = 0x101000

// hdlMagic marks the start of the HDL header. hdl_dump checks for it before
// trusting the extent table (hdl.c: hdl_read_game_alloc_table).
const hdlMagic = 0xdeadfeed

// Field offsets inside the 1 KiB HDL header, from hdl_ginfo_read in hdl.c.
const (
	hdlOffName      = 0x0008 // NUL-terminated game name
	hdlOffCompat    = 0x00a9 // compatibility flag bits
	hdlOffDMA       = 0x00aa // DMA mode word
	hdlOffStartup   = 0x00ac // "SLUS_209.46"
	hdlOffMediaType = 0x00ec // 0x14 means DVD
	hdlOffNumParts  = 0x00f0 // number of image extents that follow
	hdlOffPartTable = 0x00f5 // 12-byte entries: start, ?, length
)

// GameInfo is one installed HDLoader title as recorded on the HDD.
type GameInfo struct {
	PartitionName string
	Name          string
	Startup       string // OPL-form serial, e.g. SLUS_209.46
	CompatFlags   uint8
	DMA           uint16
	IsDVD         bool
	Slice         int
	StartSector   uint32

	// RawSizeKB is the size of the game image itself; AllocSizeKB is the space
	// the APA partition and its extents consume. The second is always rounded
	// up to whole 128 MiB chunks and so is larger.
	RawSizeKB   uint32
	AllocSizeKB uint32
}

// SizeBytes is the on-HDD footprint of the title.
func (g GameInfo) SizeBytes() int64 { return int64(g.AllocSizeKB) * 1024 }

// ImageBytes is the size of the game image itself.
func (g GameInfo) ImageBytes() int64 { return int64(g.RawSizeKB) * 1024 }

// CompatFlagList renders the compatibility bits the way hdl_dump prints them,
// e.g. "+1+3", or "0" when none are set.
func (g GameInfo) CompatFlagList() string {
	var b strings.Builder
	for i := 0; i < 8; i++ {
		if g.CompatFlags&(1<<i) != 0 {
			fmt.Fprintf(&b, "+%d", i+1)
		}
	}
	if b.Len() == 0 {
		return "0"
	}
	return b.String()
}

// DMAMode renders the DMA setting as hdl_dump does: "*u4" for UDMA 4, "*m2"
// for MDMA 2, or "" when the stored value matches neither encoding.
func (g GameInfo) DMAMode() string {
	switch {
	case g.DMA%256 == 32:
		if m := (g.DMA - 32) / 256; m < 3 {
			return fmt.Sprintf("*m%d", m)
		}
	case g.DMA%256 == 64:
		if u := (g.DMA - 64) / 256; u < 5 {
			return fmt.Sprintf("*u%d", u)
		}
	}
	return ""
}

// ReadGames returns every HDLoader title in the table, in on-disk order.
func ReadGames(r io.ReaderAt, toc *TOC) ([]GameInfo, error) {
	var out []GameInfo
	for _, s := range toc.Slices {
		for _, p := range s.Partitions {
			// hdl_dump counts a partition as a game when flags is exactly zero
			// and the type is 0x1337; sub-extents carry the same type but a
			// non-zero flags word.
			if p.Flags != 0 || p.Type != TypeHDL {
				continue
			}
			g, err := ReadGameInfo(r, s.Index, p)
			if err != nil {
				return nil, fmt.Errorf("read HDL header of %q: %w", p.ID, err)
			}
			out = append(out, g)
		}
	}
	return out, nil
}

// ReadGameInfo decodes the HDL descriptor stored inside one game partition.
func ReadGameInfo(r io.ReaderAt, sliceIndex int, p Header) (GameInfo, error) {
	buf := make([]byte, HeaderSize)
	off := int64(sliceIndex)*slice2Offset*SectorSize + int64(p.Start)*SectorSize + hdlHeaderOffset
	if _, err := readFull(r, buf, off); err != nil {
		return GameInfo{}, err
	}

	if got := binary.LittleEndian.Uint32(buf); got != hdlMagic {
		return GameInfo{}, fmt.Errorf("partition %q is typed as an HDLoader game but has no HDL header (magic %#08x)", p.ID, got)
	}

	g := GameInfo{
		PartitionName: p.ID,
		Name:          trimField(buf[hdlOffName:hdlOffCompat]),
		Startup:       trimField(buf[hdlOffStartup : hdlOffStartup+13]),
		CompatFlags:   buf[hdlOffCompat],
		DMA:           binary.LittleEndian.Uint16(buf[hdlOffDMA:]),
		IsDVD:         buf[hdlOffMediaType] == 0x14,
		Slice:         sliceIndex,
		StartSector:   p.Start,
		AllocSizeKB:   uint32(p.TotalSectors() / 2),
	}

	// The extent table records the image in 2048-byte units, hence /4 rather
	// than /2 when converting the total to kilobytes.
	numParts := int(buf[hdlOffNumParts])
	var raw uint32
	for i := 0; i < numParts; i++ {
		off := hdlOffPartTable + i*12 + 8
		if off+4 > len(buf) {
			return g, fmt.Errorf("%w: extent table claims %d extents but overruns the header", ErrBadAPA, numParts)
		}
		raw += binary.LittleEndian.Uint32(buf[off:])
	}
	g.RawSizeKB = raw / 4
	return g, nil
}

// PartitionName builds the APA partition id hdl_dump would use for a game.
// hdl_dump truncates the game name so that prefix + startup + '.' + name fits
// the 32-byte id field (hdl.c: hdl_pname).
func PartitionName(startup, name string, hidden bool) string {
	prefix := "PP."
	if hidden {
		prefix = "__."
	}
	base := prefix + startup + "."
	room := 32 - len(base)
	if room < 0 {
		room = 0
	}
	if len(name) > room {
		name = name[:room]
	}
	return base + name
}
