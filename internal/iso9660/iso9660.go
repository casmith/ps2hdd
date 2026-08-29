// Package iso9660 reads just enough of an ISO 9660 filesystem to find and read
// a small file, which for ps2hdd means SYSTEM.CNF.
//
// It works over an abstract sector source so the same code serves a PS2 ISO
// (2048-byte user data per sector, no headers) and a PS1 BIN ripped as
// MODE2/2352 (2352-byte sectors with 24 bytes of sync, header and subheader in
// front of the 2048 bytes of user data).
package iso9660

import (
	"errors"
	"fmt"
	"io"
	"strings"
)

// LogicalSectorSize is the ISO 9660 user-data sector size.
const LogicalSectorSize = 2048

// ErrNotISO means the image has no ISO 9660 primary volume descriptor at the
// usual place.
var ErrNotISO = errors.New("no ISO 9660 primary volume descriptor")

// ErrNotFound means the requested path is not in the root directory.
var ErrNotFound = errors.New("file not found in image")

// SectorReader hands back the 2048 bytes of user data for a logical block.
type SectorReader interface {
	// ReadSector fills buf (2048 bytes) with the user data of logical block lba.
	ReadSector(lba uint32, buf []byte) error
}

// RawSectorReader reads an image whose sectors are laid out back to back with
// a fixed stride and a fixed offset to the user data.
//
//	PS2 ISO / PS1 MODE1-2048:  stride 2048, offset 0
//	PS1 BIN MODE2/2352:        stride 2352, offset 24
type RawSectorReader struct {
	R      io.ReaderAt
	Stride int64
	Offset int64
}

// ReadSector implements SectorReader.
func (r RawSectorReader) ReadSector(lba uint32, buf []byte) error {
	if len(buf) != LogicalSectorSize {
		return fmt.Errorf("sector buffer is %d bytes, want %d", len(buf), LogicalSectorSize)
	}
	off := int64(lba)*r.Stride + r.Offset
	n, err := r.R.ReadAt(buf, off)
	if err != nil && n == len(buf) {
		return nil
	}
	return err
}

// Mode2352 is a RawSectorReader for a MODE2/2352 CD image.
func Mode2352(r io.ReaderAt) RawSectorReader {
	return RawSectorReader{R: r, Stride: 2352, Offset: 24}
}

// Mode2048 is a RawSectorReader for a plain 2048-byte-per-sector image.
func Mode2048(r io.ReaderAt) RawSectorReader {
	return RawSectorReader{R: r, Stride: LogicalSectorSize, Offset: 0}
}

// Volume is a parsed primary volume descriptor plus the location of the root
// directory.
type Volume struct {
	VolumeID    string
	SystemID    string
	TotalBlocks uint32

	rootLBA  uint32
	rootSize uint32
	r        SectorReader
}

// SizeBytes is the volume space size in bytes, i.e. how much of the medium the
// filesystem claims to occupy.
func (v *Volume) SizeBytes() int64 { return int64(v.TotalBlocks) * LogicalSectorSize }

// Open reads the primary volume descriptor. ISO 9660 puts the descriptor set
// at logical block 16.
func Open(r SectorReader) (*Volume, error) {
	buf := make([]byte, LogicalSectorSize)
	for lba := uint32(16); lba < 16+8; lba++ {
		if err := r.ReadSector(lba, buf); err != nil {
			return nil, fmt.Errorf("read volume descriptor at block %d: %w", lba, err)
		}
		if string(buf[1:6]) != "CD001" {
			continue
		}
		switch buf[0] {
		case 0x01: // primary volume descriptor
			v := &Volume{
				SystemID:    strings.TrimSpace(string(buf[8:40])),
				VolumeID:    strings.TrimSpace(string(buf[40:72])),
				TotalBlocks: leU32(buf[80:]),
				r:           r,
			}
			// The root directory record is embedded at offset 156 and uses the
			// same layout as any other directory record.
			rec := buf[156 : 156+34]
			v.rootLBA = leU32(rec[2:])
			v.rootSize = leU32(rec[10:])
			return v, nil
		case 0xff: // volume descriptor set terminator
			return nil, ErrNotISO
		}
	}
	return nil, ErrNotISO
}

// dirEntry is one parsed directory record.
type dirEntry struct {
	Name  string
	LBA   uint32
	Size  uint32
	IsDir bool
}

// ReadDir lists the root directory. ps2hdd only ever needs root-level files,
// so subdirectory traversal is deliberately not implemented.
func (v *Volume) ReadDir() ([]string, error) {
	entries, err := v.readRoot()
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name)
	}
	return names, nil
}

func (v *Volume) readRoot() ([]dirEntry, error) {
	if v.rootSize == 0 {
		return nil, ErrNotISO
	}
	blocks := (v.rootSize + LogicalSectorSize - 1) / LogicalSectorSize
	// A root directory larger than a megabyte is not something a PS1 or PS2
	// disc has; treat it as corruption rather than reading forever.
	if blocks > 512 {
		return nil, fmt.Errorf("root directory claims %d bytes, which is implausible", v.rootSize)
	}

	var out []dirEntry
	buf := make([]byte, LogicalSectorSize)
	for b := uint32(0); b < blocks; b++ {
		if err := v.r.ReadSector(v.rootLBA+b, buf); err != nil {
			return nil, fmt.Errorf("read root directory block %d: %w", b, err)
		}
		for off := 0; off < len(buf); {
			recLen := int(buf[off])
			if recLen == 0 {
				break // rest of this block is padding
			}
			if off+recLen > len(buf) || recLen < 33 {
				break
			}
			rec := buf[off : off+recLen]
			nameLen := int(rec[32])
			if 33+nameLen > recLen {
				break
			}
			name := string(rec[33 : 33+nameLen])
			// Records 0 and 1 are "." and ".." encoded as single NUL bytes.
			if nameLen == 1 && (name[0] == 0 || name[0] == 1) {
				off += recLen
				continue
			}
			out = append(out, dirEntry{
				Name:  name,
				LBA:   leU32(rec[2:]),
				Size:  leU32(rec[10:]),
				IsDir: rec[25]&0x02 != 0,
			})
			off += recLen
		}
	}
	return out, nil
}

// maxFileBytes caps ReadFile. SYSTEM.CNF is a few hundred bytes; anything
// approaching this limit is not a file this package should be reading.
const maxFileBytes = 1 << 20

// ReadFile reads a file from the root directory. Matching ignores case and the
// ";1" version suffix ISO 9660 appends to every filename.
func (v *Volume) ReadFile(name string) ([]byte, error) {
	entries, err := v.readRoot()
	if err != nil {
		return nil, err
	}
	want := strings.ToUpper(name)
	for _, e := range entries {
		if e.IsDir {
			continue
		}
		if !strings.EqualFold(stripVersion(e.Name), want) {
			continue
		}
		if e.Size > maxFileBytes {
			return nil, fmt.Errorf("%s is %d bytes, which is too large to read as metadata", name, e.Size)
		}
		out := make([]byte, 0, e.Size)
		buf := make([]byte, LogicalSectorSize)
		for remaining, lba := e.Size, e.LBA; remaining > 0; lba++ {
			if err := v.r.ReadSector(lba, buf); err != nil {
				return nil, fmt.Errorf("read %s at block %d: %w", name, lba, err)
			}
			n := uint32(LogicalSectorSize)
			if remaining < n {
				n = remaining
			}
			out = append(out, buf[:n]...)
			remaining -= n
		}
		return out, nil
	}
	return nil, fmt.Errorf("%w: %s", ErrNotFound, name)
}

func stripVersion(name string) string {
	if i := strings.IndexByte(name, ';'); i >= 0 {
		return name[:i]
	}
	return name
}

func leU32(b []byte) uint32 {
	return uint32(b[0]) | uint32(b[1])<<8 | uint32(b[2])<<16 | uint32(b[3])<<24
}
