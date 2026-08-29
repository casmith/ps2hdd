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
}

// Layout constants. Blocks 0-15 are the system area, 16 is the primary volume
// descriptor, 17 the terminator, 18 the root directory, and file data starts
// at 19.
const (
	pvdLBA        = 16
	terminatorLBA = 17
	rootLBA       = 18
	firstFileLBA  = 19
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
	copy(pvd[156:156+34], rootRecord())

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
// The sync pattern and headers are filled in well enough to look like a real
// dump to anything that inspects them.
func BuildMode2352(img Image) ([]byte, error) {
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
