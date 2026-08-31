package ps1

import (
	"encoding/binary"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// SectorSize is the raw CD sector size of a MODE2/2352 rip, which is the only
// input format POPS accepts.
const SectorSize = 2352

// HeaderSize is the size of the POPS header that precedes the disc data in a
// VCD file.
const HeaderSize = 0x100000

// leadInSectors is the standard 2-second (150-frame) lead-in.
const leadInSectors = 150

// The VCD container is a 1 MiB POPS header followed by the raw MODE2/2352
// stream. The header holds a small table of contents: three descriptors (A0,
// A1, A2) in the first 30 bytes, then a 10-byte entry per track, then the
// sector count at 1032 and 1036.
//
// The layout and the timecode adjustments below were derived from cue2pops
// v2.0 (github.com/makefu/cue2pops-linux, cue2pops.c), which is the reference
// converter the PS2 homebrew community uses. ps2hdd implements the conversion
// natively so that installing a PS1 game does not require a second external
// tool, and so that the conversion can be unit tested; a user who prefers the
// original can point `tools.cue2pops` at it and ps2hdd will shell out instead.
// See docs/compatibility.md.
const (
	offDiscTypeDesc = 0x00 // A0 descriptor
	offContentDesc  = 0x0a // A1 descriptor
	offLeadOutDesc  = 0x14 // A2 descriptor
	offFirstTrack   = 0x1e // first 10-byte track entry
	offVersionIdent = 1024
	offSectorCount1 = 1032
	offSectorCount2 = 1036
)

// Track entry types as the POPS header encodes them.
const (
	trackTypeData  = 0x41
	trackTypeAudio = 0x01
)

// ConvertOptions tune the conversion.
type ConvertOptions struct {
	// OnProgress receives a 0..1 fraction as the disc data is copied.
	OnProgress func(fraction float64)
}

// BuildHeader renders the 1 MiB POPS header for a cuesheet describing a BIN of
// binSize bytes.
//
// It is separated from Convert so the header — the part that decides whether
// the resulting VCD boots — can be asserted byte for byte in tests without
// materialising a multi-hundred-megabyte image.
func BuildHeader(c Cue, binSize int64) ([]byte, error) {
	if err := c.Validate(); err != nil {
		return nil, err
	}
	if binSize <= 0 || binSize%SectorSize != 0 {
		return nil, fmt.Errorf("%w: BIN is %d bytes, not a whole number of %d-byte sectors", ErrBadCue, binSize, SectorSize)
	}
	h := make([]byte, HeaderSize)

	// A0: disc type. Track 1 of a PlayStation disc is always data, the first
	// track number is 1, and the disc type byte is 0x20 for CD-XA.
	h[offDiscTypeDesc+0] = trackTypeData
	h[offDiscTypeDesc+2] = 0xa0
	h[offDiscTypeDesc+7] = 0x01
	h[offDiscTypeDesc+8] = 0x20

	// A1: content. The type byte records the *last* track's type, so a disc
	// with CD-DA reads as mixed-mode; the count byte is the last track number.
	h[offContentDesc+2] = 0xa1

	// A2: lead-out.
	h[offLeadOutDesc+2] = 0xa2

	for i, t := range c.Tracks {
		entry := offFirstTrack + i*10
		if entry+10 > offVersionIdent {
			return nil, fmt.Errorf("%w: %d tracks do not fit in the POPS header", ErrBadCue, len(c.Tracks))
		}
		typ := byte(trackTypeData)
		if t.IsAudio() {
			typ = trackTypeAudio
		}
		h[entry+0] = typ
		h[entry+2] = bcd(t.Number)

		// Both index fields default to INDEX 01; INDEX 00 overrides the first
		// three bytes when the sheet declares a pregap position.
		idx0 := t.Index1
		if t.HasIndex0 {
			idx0 = t.Index0
		}
		idx1 := t.Index1

		// Every track's INDEX 01 is shifted by the 2-second lead-in. INDEX 00
		// is shifted too, except on track 1 where it coincides with the start
		// of the disc.
		if i != 0 {
			idx0 = idx0.plus2Seconds()
		}
		idx1 = idx1.plus2Seconds()

		// A CDRWIN-style sheet declares its pregap as a PREGAP command rather
		// than including it in the image, so the pregap is materialised during
		// the copy and every later timecode moves another 2 seconds.
		if c.LooksLikeCDRWIN() && i != 0 {
			idx0 = idx0.plus2Seconds()
			idx1 = idx1.plus2Seconds()
		}

		h[entry+3], h[entry+4], h[entry+5] = idx0.BCD()
		h[entry+7], h[entry+8], h[entry+9] = idx1.BCD()

		h[offContentDesc+0] = typ
		h[offLeadOutDesc+0] = typ
		h[offContentDesc+7] = bcd(t.Number)
	}

	// The sector count written into the header excludes the lead-in; the
	// lead-out timecode includes it.
	gapSectors := leadInSectors * (c.PregapCount() + c.PostgapCount())
	sectorCount := uint32(binSize/SectorSize) + uint32(gapSectors)
	leadOut := lbaToMSF(int(sectorCount) + leadInSectors)
	h[offLeadOutDesc+7], h[offLeadOutDesc+8], h[offLeadOutDesc+9] = leadOut.BCD()

	copy(h[offVersionIdent:], []byte{0x6b, 0x48, 0x6e, 0x20})
	binary.LittleEndian.PutUint32(h[offSectorCount1:], sectorCount)
	binary.LittleEndian.PutUint32(h[offSectorCount2:], sectorCount)
	return h, nil
}

// plus2Seconds advances a timecode by the 2-second lead-in, carrying into
// minutes.
func (t MSF) plus2Seconds() MSF {
	t.S += 2
	if t.S >= 60 {
		t.S -= 60
		t.M++
	}
	return t
}

func lbaToMSF(lba int) MSF {
	if lba < 0 {
		lba = 0
	}
	return MSF{M: lba / 4500, S: (lba % 4500) / 75, F: lba % 75}
}

// VCDSize reports the size of the VCD a rip of this shape converts into: the
// POPS header, every track file, and any gap the conversion materialises.
func VCDSize(sourceBytes int64, gapSectors int) int64 {
	if sourceBytes <= 0 {
		return 0
	}
	return HeaderSize + sourceBytes + int64(gapSectors)*SectorSize
}

// PadOffset reports the byte offset in the BIN at which a CDRWIN pregap must
// be inserted, and whether one is needed at all.
//
// cue2pops places the pregap immediately before the first audio track, using
// its INDEX 00 when the sheet has one and its INDEX 01 otherwise. A disc with
// no audio track at all gets the padding appended at the end.
func PadOffset(c Cue, binSize int64) (int64, bool) {
	if !c.LooksLikeCDRWIN() {
		return 0, false
	}
	for _, t := range c.Tracks {
		if !t.IsAudio() {
			continue
		}
		at := t.Index1
		if t.HasIndex0 {
			at = t.Index0
		}
		off := int64(at.LBA()) * SectorSize
		if off > binSize {
			off = binSize
		}
		return off, true
	}
	return binSize, true
}

// Convert writes a VCD for the cuesheet to outPath.
//
// The output is the POPS header followed by the disc data, with a 2-second
// pregap materialised for CDRWIN-style sheets. No game-specific patching is
// performed: cue2pops can optionally apply per-title cheats and a PAL-to-NTSC
// video patch, and ps2hdd deliberately does neither, since silently altering a
// game's code is not something an install tool should do without being asked.
func Convert(cuePath, outPath string, opts ConvertOptions) error {
	c, err := ParseCueFile(cuePath)
	if err != nil {
		return err
	}
	if err := c.Validate(); err != nil {
		return err
	}
	// A split rip is joined on the way through rather than merged on disk
	// first. The tracks are concatenated in sheet order, which is what the
	// format means, so there is nothing to gain from writing a second copy of
	// several hundred megabytes and then reading it straight back.
	src, binSize, closeAll, err := openTracks(c)
	if err != nil {
		return err
	}
	defer closeAll()
	if c.Split() {
		joined, err := c.Joined(trackSizes(c))
		if err != nil {
			return err
		}
		c = joined
	}
	bin := src

	header, err := BuildHeader(c, binSize)
	if err != nil {
		return err
	}

	if err := os.MkdirAll(filepath.Dir(outPath), 0o755); err != nil {
		return err
	}
	// Write to a temporary name and rename on success so an interrupted
	// conversion never leaves a half-written VCD that looks installable.
	tmp := outPath + ".partial"
	out, err := os.Create(tmp)
	if err != nil {
		return err
	}
	defer func() {
		out.Close()
		os.Remove(tmp)
	}()

	if _, err := out.Write(header); err != nil {
		return err
	}
	padAt, needPad := PadOffset(c, binSize)
	if err := copyWithPad(out, bin, binSize, padAt, needPad, opts.OnProgress); err != nil {
		return err
	}
	if err := out.Sync(); err != nil {
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, outPath); err != nil {
		return err
	}
	return nil
}

// copyBufSize is the streaming chunk size. VCDs run to hundreds of megabytes,
// so the image is never held in memory.
const copyBufSize = 4 << 20

func copyWithPad(dst io.Writer, src io.Reader, total, padAt int64, needPad bool, onProgress func(float64)) error {
	buf := make([]byte, copyBufSize)
	var done int64
	report := func() {
		if onProgress != nil && total > 0 {
			onProgress(float64(done) / float64(total))
		}
	}
	writePad := func() error {
		pad := make([]byte, leadInSectors*SectorSize)
		_, err := dst.Write(pad)
		return err
	}

	for done < total {
		n := int64(len(buf))
		if remaining := total - done; remaining < n {
			n = remaining
		}
		if needPad && done < padAt && done+n > padAt {
			n = padAt - done
		}
		if _, err := io.ReadFull(src, buf[:n]); err != nil {
			return fmt.Errorf("read disc image at offset %d: %w", done, err)
		}
		if _, err := dst.Write(buf[:n]); err != nil {
			return err
		}
		done += n
		if needPad && done == padAt {
			if err := writePad(); err != nil {
				return err
			}
			needPad = false
		}
		report()
	}
	if needPad {
		if err := writePad(); err != nil {
			return err
		}
	}
	return nil
}

// trackSizes reports each data file's size, in sheet order.
//
// It is separate from openTracks because Joined needs the sizes before the
// sheet is rewritten, and openTracks has already stat'd them.
func trackSizes(c Cue) []int64 {
	out := make([]int64, 0, len(c.FilePaths))
	for _, p := range c.FilePaths {
		fi, err := os.Stat(p)
		if err != nil {
			return out
		}
		out = append(out, fi.Size())
	}
	return out
}

// openTracks opens every data file the sheet names and returns them as one
// stream, along with the total size.
//
// A single-file sheet is the same code path with one file in it, which is what
// keeps the split case from being a parallel universe with its own bugs.
func openTracks(c Cue) (io.Reader, int64, func(), error) {
	paths := c.FilePaths
	if len(paths) == 0 && c.BinPath != "" {
		paths = []string{c.BinPath}
	}
	if len(paths) == 0 {
		return nil, 0, func() {}, fmt.Errorf("%w: %s names no data file", ErrBadCue, filepath.Base(c.Path))
	}

	var (
		files  []*os.File
		rs     []io.Reader
		total  int64
		closer = func() {}
	)
	closeAll := func() {
		for _, f := range files {
			f.Close()
		}
	}
	for i, p := range paths {
		f, err := os.Open(p)
		if err != nil {
			closeAll()
			name := p
			if i < len(c.Files) {
				name = c.Files[i]
			}
			return nil, 0, closer, fmt.Errorf("open %s: %w", filepath.Base(name), err)
		}
		fi, err := f.Stat()
		if err != nil {
			f.Close()
			closeAll()
			return nil, 0, closer, err
		}
		files = append(files, f)
		rs = append(rs, f)
		total += fi.Size()
	}
	return io.MultiReader(rs...), total, closeAll, nil
}
