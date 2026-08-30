// Package isosynth builds minimal ISO 9660 images for tests and demo mode.
//
// The images are just large enough to exercise the reader in
// internal/iso9660: a primary volume descriptor, a terminator, a single-block
// root directory and a handful of root-level files. They are never produced on
// a user's behalf; ps2hdd only ever reads real images.
package isosynth

import (
	"bytes"
	"fmt"
	"sort"
	"strings"

	"github.com/casmith/ps2hdd/internal/iso9660"
)

// Image describes an ISO to build.
type Image struct {
	VolumeID string
	// Files are root-level files, keyed by ISO name (without the ";1" suffix,
	// which is appended automatically).
	Files map[string][]byte
	// PadBlocks extends the image past its content, which is how a test makes
	// an image big enough to be classified as a DVD.
	PadBlocks uint32
	// CDXA writes the CD-ROM XA signature into the volume descriptor. Every
	// real PlayStation CD carries it and no DVD does, so it is what decides
	// the media type; leaving it false produces a DVD-shaped image.
	CDXA bool
}

// Layout constants. Blocks 0-15 are the system area, 16 is the primary volume
// descriptor, 17 the terminator, 18 and 19 the two path tables, 20 the root
// directory, and file data starts at 21.
const (
	pvdLBA        = 16
	terminatorLBA = 17
	pathTableLLBA = 18
	pathTableMLBA = 19
	rootLBA       = 20
	firstFileLBA  = 21
)

// Build renders the image as a byte slice of 2048-byte logical blocks.
func Build(img Image) ([]byte, error) {
	if img.VolumeID == "" {
		img.VolumeID = "PS2HDD_TEST"
	}
	names := make([]string, 0, len(img.Files))
	for n := range img.Files {
		names = append(names, n)
	}
	sort.Strings(names)

	type placed struct {
		name string
		lba  uint32
		size uint32
		data []byte
	}
	var files []placed
	lba := uint32(firstFileLBA)
	for _, n := range names {
		data := img.Files[n]
		files = append(files, placed{name: n, lba: lba, size: uint32(len(data)), data: data})
		blocks := uint32((len(data) + iso9660.LogicalSectorSize - 1) / iso9660.LogicalSectorSize)
		if blocks == 0 {
			blocks = 1
		}
		lba += blocks
	}
	totalBlocks := lba + img.PadBlocks

	// Root directory: "." and ".." then one record per file.
	root := &bytes.Buffer{}
	writeDirRecord(root, "\x00", rootLBA, iso9660.LogicalSectorSize, true)
	writeDirRecord(root, "\x01", rootLBA, iso9660.LogicalSectorSize, true)
	for _, f := range files {
		writeDirRecord(root, strings.ToUpper(f.name)+";1", f.lba, f.size, false)
	}
	if root.Len() > iso9660.LogicalSectorSize {
		return nil, fmt.Errorf("synthetic root directory needs %d bytes, more than one block", root.Len())
	}

	out := make([]byte, int(totalBlocks)*iso9660.LogicalSectorSize)

	pvd := out[pvdLBA*iso9660.LogicalSectorSize:][:iso9660.LogicalSectorSize]
	pvd[0] = 0x01
	copy(pvd[1:6], "CD001")
	pvd[6] = 0x01
	padCopy(pvd[8:40], "PLAYSTATION")
	padCopy(pvd[40:72], img.VolumeID)
	putBoth32(pvd[80:], totalBlocks)
	putBoth16(pvd[120:], 1) // volume set size
	putBoth16(pvd[124:], 1) // volume sequence number
	putBoth16(pvd[128:], iso9660.LogicalSectorSize)

	// Path tables. internal/iso9660 does not need these -- it finds the root
	// directory through the record embedded in the PVD at offset 156, which is
	// equally valid and is what most readers use. Real discs always carry path
	// tables, though, and some tools navigate exclusively through them:
	// hdl_dump does, and rejects an image without one as "bad ISOFS". Writing
	// them keeps these fixtures realistic enough for an independent
	// implementation to read, which is what makes cross-validation possible.
	putBoth32(pvd[132:], pathTableSize) // path table size, both-endian
	putLE32(pvd[140:], pathTableLLBA)   // type-L path table
	putLE32(pvd[144:], 0)               // optional type-L path table
	putBE32(pvd[148:], pathTableMLBA)   // type-M path table
	putBE32(pvd[152:], 0)               // optional type-M path table

	if img.CDXA {
		copy(pvd[1024:1032], iso9660.XASignature)
	}

	copy(pvd[156:156+34], rootRecord())

	copy(out[pathTableLLBA*iso9660.LogicalSectorSize:], pathTableRecord(false))
	copy(out[pathTableMLBA*iso9660.LogicalSectorSize:], pathTableRecord(true))

	term := out[terminatorLBA*iso9660.LogicalSectorSize:][:iso9660.LogicalSectorSize]
	term[0] = 0xff
	copy(term[1:6], "CD001")
	term[6] = 0x01

	copy(out[rootLBA*iso9660.LogicalSectorSize:], root.Bytes())
	for _, f := range files {
		copy(out[int(f.lba)*iso9660.LogicalSectorSize:], f.data)
	}
	return out, nil
}

// BuildMode2352 renders the image as a MODE2/2352 CD dump, which is what a PS1
// BIN file is: each 2048-byte block sits at offset 24 of a 2352-byte sector.
// PS1 discs are always CDs, so the XA signature is written unconditionally.
// The sync pattern and headers are filled in well enough to look like a real
// dump to anything that inspects them.
func BuildMode2352(img Image) ([]byte, error) {
	img.CDXA = true
	data, err := Build(img)
	if err != nil {
		return nil, err
	}
	blocks := len(data) / iso9660.LogicalSectorSize
	out := make([]byte, blocks*2352)
	for i := 0; i < blocks; i++ {
		sec := out[i*2352:][:2352]
		// 12-byte sync pattern.
		sec[0] = 0x00
		for j := 1; j < 11; j++ {
			sec[j] = 0xff
		}
		sec[11] = 0x00
		// 4-byte header: MSF of the sector (offset by the 150-sector lead-in)
		// in BCD, then the mode.
		m, s, f := lbaToMSF(i + 150)
		sec[12], sec[13], sec[14] = bcd(m), bcd(s), bcd(f)
		sec[15] = 0x02 // Mode 2
		// 8-byte subheader, duplicated as the standard requires. 0x08 in the
		// submode byte marks a Form 1 data sector.
		sec[18], sec[22] = 0x08, 0x08
		copy(sec[24:24+iso9660.LogicalSectorSize], data[i*iso9660.LogicalSectorSize:])
	}
	return out, nil
}

func lbaToMSF(lba int) (int, int, int) {
	return lba / (60 * 75), (lba / 75) % 60, lba % 75
}

func bcd(v int) byte { return byte((v/10)<<4 | v%10) }

// pathTableSize is the byte length of the single root-directory record these
// images carry: 8 bytes of fixed fields, a one-byte identifier, and a pad byte
// to an even length.
const pathTableSize = 10

// pathTableRecord renders the path table. A root-only disc needs exactly one
// record, whose identifier is a single NUL byte -- that NUL is how a reader
// recognises the root (hdl_dump's isofs_get_root_addr tests for it).
//
// The type-L table stores its numbers little-endian and the type-M table
// big-endian; the two are otherwise identical.
func pathTableRecord(bigEndian bool) []byte {
	rec := make([]byte, pathTableSize)
	rec[0] = 1 // length of the directory identifier
	rec[1] = 0 // extended attribute record length
	if bigEndian {
		putBE32(rec[2:], rootLBA)
		putBE16(rec[6:], 1) // the root's parent is itself, directory number 1
	} else {
		putLE32(rec[2:], rootLBA)
		putLE16(rec[6:], 1)
	}
	rec[8] = 0x00 // the root directory identifier
	return rec
}

func rootRecord() []byte {
	b := &bytes.Buffer{}
	writeDirRecord(b, "\x00", rootLBA, iso9660.LogicalSectorSize, true)
	rec := b.Bytes()
	out := make([]byte, 34)
	copy(out, rec)
	out[0] = 34
	return out
}

func writeDirRecord(b *bytes.Buffer, name string, lba, size uint32, isDir bool) {
	recLen := 33 + len(name)
	if recLen%2 != 0 {
		recLen++ // records are padded to an even length
	}
	rec := make([]byte, recLen)
	rec[0] = byte(recLen)
	putBoth32(rec[2:], lba)
	putBoth32(rec[10:], size)
	if isDir {
		rec[25] = 0x02
	}
	putBoth16(rec[28:], 1) // volume sequence number
	rec[32] = byte(len(name))
	copy(rec[33:], name)
	b.Write(rec)
}

// putBoth32 writes an ISO 9660 "both-endian" 32-bit field.
func putBoth32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	b[4], b[5], b[6], b[7] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

// putBoth16 writes an ISO 9660 "both-endian" 16-bit field.
func putBoth16(b []byte, v uint16) {
	b[0], b[1] = byte(v), byte(v>>8)
	b[2], b[3] = byte(v>>8), byte(v)
}

// putLE32, putBE32, putLE16 and putBE16 write single-endian fields, which the
// path table and its PVD pointers use rather than the both-endian form.
func putLE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
}

func putBE32(b []byte, v uint32) {
	b[0], b[1], b[2], b[3] = byte(v>>24), byte(v>>16), byte(v>>8), byte(v)
}

func putLE16(b []byte, v uint16) { b[0], b[1] = byte(v), byte(v>>8) }

func putBE16(b []byte, v uint16) { b[0], b[1] = byte(v>>8), byte(v) }

func padCopy(dst []byte, s string) {
	for i := range dst {
		dst[i] = ' '
	}
	copy(dst, s)
}

// PS2SystemCNF returns the SYSTEM.CNF contents of a PS2 disc.
func PS2SystemCNF(serial string) []byte {
	return []byte("BOOT2 = cdrom0:\\" + serial + ";1\r\nVER = 1.00\r\nVMODE = NTSC\r\n")
}

// PS1SystemCNF returns the SYSTEM.CNF contents of a PS1 disc.
func PS1SystemCNF(serial string) []byte {
	return []byte("BOOT = cdrom:\\" + serial + ";1\r\nTCB = 4\r\nEVENT = 10\r\nSTACK = 801FFFF0\r\n")
}
