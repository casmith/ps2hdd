// Package apasynth builds synthetic APA disk images.
//
// It exists so that the whole read path — partition walking, HDL header
// decoding, PFS discovery, the drive view, the catalog and the TUI — can be
// exercised without a physical PS2 HDD, both from `go test` and from
// `ps2hdd --demo`. It is a writer for *test fixtures only*: ps2hdd never
// writes APA structures to a real device, and nothing in the install or remove
// paths calls into this package.
package apasynth

import (
	"encoding/binary"
	"fmt"
	"os"
	"strings"

	"github.com/casmith/ps2hdd/internal/apa"
)

// Game describes an HDLoader title to place in a synthetic image.
type Game struct {
	Name    string `json:"name"`
	Startup string `json:"startup"` // OPL-form serial, e.g. SLUS_209.46
	SizeMB  uint32 `json:"size_mb"` // rounded up to a whole 128 MiB chunk
	IsDVD   bool   `json:"is_dvd"`
}

// PFSPart describes a non-HDL partition such as +OPL or __.POPS.
type PFSPart struct {
	ID     string `json:"id"`
	SizeMB uint32 `json:"size_mb"`
}

// Disk describes an image to build.
type Disk struct {
	SizeMB uint32    `json:"size_mb"`
	Parts  []PFSPart `json:"parts"`
	Games  []Game    `json:"games"`
}

// DefaultDisk is a plausible 120 GB PS2 HDD: the system partitions a stock
// FreeHDBoot install creates, the OPL and POPS partitions, and a few games.
func DefaultDisk() Disk {
	return Disk{
		SizeMB: 114473, // ~120 GB, the classic PS2-compatible size
		Parts: []PFSPart{
			{ID: "__net", SizeMB: 128},
			{ID: "__system", SizeMB: 256},
			{ID: "__sysconf", SizeMB: 128},
			{ID: "__common", SizeMB: 128},
			{ID: "+OPL", SizeMB: 512},
			{ID: "__.POPS", SizeMB: 8192},
		},
		Games: []Game{
			{Name: "Burnout 3 Takedown", Startup: "SLUS_210.50", SizeMB: 3456, IsDVD: true},
			{Name: "God Hand", Startup: "SLUS_215.03", SizeMB: 2944, IsDVD: true},
			{Name: "Ridge Racer V", Startup: "SLUS_200.02", SizeMB: 640, IsDVD: false},
		},
	}
}

// Write builds the image at path. The file is sparse: only sectors that hold
// structures are written, so a nominally 120 GB image costs a few hundred
// kilobytes of disk.
func Write(path string, d Disk) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := WriteTo(f, d); err != nil {
		return err
	}
	return f.Close()
}

// WriteTo builds the image into an already-open file.
func WriteTo(f *os.File, d Disk) error {
	if d.SizeMB == 0 {
		d.SizeMB = DefaultDisk().SizeMB
	}
	total := int64(d.SizeMB) * 1024 * 1024
	if err := f.Truncate(total); err != nil {
		return fmt.Errorf("size image: %w", err)
	}

	type placed struct {
		id     string
		typ    uint16
		start  uint32
		length uint32
		game   *Game
	}

	// The MBR partition always occupies the first chunk.
	const chunkSectors = apa.ChunkMB * 1024 * 1024 / apa.SectorSize
	var parts []placed
	next := uint32(0)
	alloc := func(id string, typ uint16, sizeMB uint32, g *Game) error {
		chunks := (sizeMB + apa.ChunkMB - 1) / apa.ChunkMB
		if chunks == 0 {
			chunks = 1
		}
		length := chunks * chunkSectors
		if uint64(next)+uint64(length) > uint64(total/apa.SectorSize) {
			return fmt.Errorf("synthetic disk is too small for partition %q", id)
		}
		parts = append(parts, placed{id: id, typ: typ, start: next, length: length, game: g})
		next += length
		return nil
	}

	if err := alloc("__mbr", apa.TypeMBR, apa.ChunkMB, nil); err != nil {
		return err
	}
	for _, p := range d.Parts {
		if err := alloc(p.ID, apa.TypePFS, p.SizeMB, nil); err != nil {
			return err
		}
	}
	for i := range d.Games {
		g := &d.Games[i]
		if err := alloc(apa.PartitionName(g.Startup, g.Name, false), apa.TypeHDL, g.SizeMB, g); err != nil {
			return err
		}
	}

	for i, p := range parts {
		hdr := make([]byte, apa.HeaderSize)
		copy(hdr[4:8], []byte{'A', 'P', 'A', 0})
		var nextSector uint32
		if i+1 < len(parts) {
			nextSector = parts[i+1].start
		}
		var prevSector uint32
		if i > 0 {
			prevSector = parts[i-1].start
		}
		binary.LittleEndian.PutUint32(hdr[8:], nextSector)
		binary.LittleEndian.PutUint32(hdr[12:], prevSector)
		copy(hdr[16:48], p.id)
		binary.LittleEndian.PutUint32(hdr[64:], p.start)
		binary.LittleEndian.PutUint32(hdr[68:], p.length)
		binary.LittleEndian.PutUint16(hdr[72:], p.typ)
		copy(hdr[128:256], p.id)
		if i == 0 {
			copy(hdr[256:288], "Sony Computer Entertainment Inc.")
			binary.LittleEndian.PutUint32(hdr[288:], 2)
			binary.LittleEndian.PutUint32(hdr[292:], uint32(total/apa.SectorSize))
		}
		setChecksum(hdr)
		if _, err := f.WriteAt(hdr, int64(p.start)*apa.SectorSize); err != nil {
			return err
		}
		if p.game != nil {
			if err := writeHDLHeader(f, p.start, *p.game); err != nil {
				return err
			}
		}
	}
	return nil
}

// writeHDLHeader lays down the descriptor hdl_dump stores 1 MiB into a game
// partition. Offsets mirror hdl_ginfo_read in hdl-dump's hdl.c.
func writeHDLHeader(f *os.File, start uint32, g Game) error {
	buf := make([]byte, apa.HeaderSize)
	binary.LittleEndian.PutUint32(buf, 0xdeadfeed)
	buf[0x0006] = 0x01
	copy(buf[0x0008:], g.Name)
	buf[0x00a9] = 0x00                                  // no compatibility flags
	binary.LittleEndian.PutUint16(buf[0x00aa:], 0x0440) // UDMA 4, as hdl_dump encodes it
	copy(buf[0x00ac:], g.Startup)
	if g.IsDVD {
		buf[0x00ec] = 0x14
	} else {
		buf[0x00ec] = 0x12
	}
	buf[0x00f0] = 1
	// One extent covering the image. hdl_dump stores the extent's start as
	// the APA start sector shifted right by 8, and its length in 256-byte
	// units (hdl.c: hdl_read_game_alloc_table divides the length by 2 to get
	// 512-byte sectors).
	binary.LittleEndian.PutUint32(buf[0x00f5+4:], start>>8)
	binary.LittleEndian.PutUint32(buf[0x00f5+8:], uint32(g.SizeMB)*4096)

	off := int64(start)*apa.SectorSize + 0x101000
	_, err := f.WriteAt(buf, off)
	return err
}

func setChecksum(hdr []byte) {
	var sum uint32
	for i := 1; i < apa.HeaderSize/4; i++ {
		sum += binary.LittleEndian.Uint32(hdr[i*4:])
	}
	binary.LittleEndian.PutUint32(hdr, sum)
}

// SanitizePartitionName reports the APA id a game name would be given, which
// tests use to assert against expected partition names.
func SanitizePartitionName(startup, name string) string {
	return apa.PartitionName(startup, strings.TrimSpace(name), false)
}
