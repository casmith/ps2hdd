// Package ps2 inspects PlayStation 2 disc images and installs them onto an
// APA HDD through hdl_dump.
package ps2

import (
	"errors"
	"fmt"
	"github.com/casmith/ps2hdd/internal/apa"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/casmith/ps2hdd/internal/iso9660"
	"github.com/casmith/ps2hdd/internal/model"
)

// ErrNotPS2 means the image has no PS2 boot record.
var ErrNotPS2 = errors.New("not a PlayStation 2 disc image")

// cdSizeLimit is the largest a PS2 CD image can plausibly be. The medium tops
// out at a 700 MB CD-ROM, so anything above this was mastered for DVD.
//
// This is only a fallback. The authoritative signal is the CD-ROM XA
// signature in the volume descriptor -- see MediaType below.
const cdSizeLimit = 750 * 1024 * 1024

// RawSectorSize is a CD sector as a ripper writes it: a 2048-byte block with
// sync, header and error-correction bytes around it.
const RawSectorSize = 2352

// Image is the result of inspecting a PS2 disc image.
type Image struct {
	Path     string
	GameID   string
	Title    string
	VolumeID string
	Media    model.MediaType
	// MediaFromSize records that the media type was inferred from the image
	// size because the volume descriptor gave no usable signature.
	MediaFromSize bool
	SizeBytes     int64
	// BootFile is the raw BOOT2 value, kept for diagnostics.
	BootFile string
}

// Game converts an inspected image into a catalog entry.
func (i Image) Game() model.Game {
	// An image does not cost its own size on an APA drive: it is rounded up
	// to whole 128 MiB chunks and charged partition overhead on top, which can
	// take another chunk. Exactly how much depends on where the drive's free
	// chunks are, and there is no drive here, so the honest figure is the
	// worst case. See apa.MaxAllocationFor.
	footprint := apa.MaxAllocationFor(i.SizeBytes)
	return model.Game{
		Platform:         model.PlatformPS2,
		Title:            i.Title,
		GameID:           i.GameID,
		SizeBytes:        i.SizeBytes,
		InstallSizeBytes: footprint,
		Media:            i.Media,
		SourcePath:       i.Path,
		Discs: []model.Disc{{
			Number:           1,
			GameID:           i.GameID,
			Title:            i.Title,
			SourcePath:       i.Path,
			SizeBytes:        i.SizeBytes,
			InstallSizeBytes: footprint,
		}},
	}
}

// Inspect reads a PS2 ISO and extracts its identity.
//
// The serial comes from SYSTEM.CNF's BOOT2 line, which is authoritative;
// filenames are only used to derive a display title, and never to decide
// identity. An image whose SYSTEM.CNF is unreadable is reported as an error
// rather than silently identified from its filename.
func Inspect(path string) (Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return Image{}, err
	}
	defer f.Close()

	fi, err := f.Stat()
	if err != nil {
		return Image{}, err
	}
	return InspectAt(f, fi.Size(), path, false)
}

// InspectAt reads a PS2 image from an io.ReaderAt.
//
// name is the path the image is known by, used for the display title and for
// error messages; size is the image's full size, which the caller knows even
// when r covers only part of it.
//
// partial says that r holds only the beginning of the image. That changes one
// thing: where the serial may come from. SYSTEM.CNF is authoritative and is
// used whenever it can be read, but on a real library it frequently sits
// gigabytes into the image -- three of eight sampled rips put it past the
// 2 GB mark -- so it is out of reach when the image is arriving as a
// decompression stream that costs real time to advance. The root directory is
// always near the front, and every PS2 disc carries its boot ELF there named
// for the serial: SLUS_202.16;1. That is the fallback, and only for a partial
// read, because a caller holding the whole image has no excuse for a guess.
func InspectAt(r io.ReaderAt, size int64, name string, partial bool) (Image, error) {
	img := Image{Path: name, SizeBytes: size}

	vol, err := openVolume(r, size)
	if err != nil {
		return img, fmt.Errorf("%s: %w: %v", filepath.Base(name), ErrNotPS2, err)
	}
	img.VolumeID = vol.VolumeID

	cnf, cnfErr := vol.ReadFile("SYSTEM.CNF")
	switch {
	case cnfErr == nil:
		boot, ok := ParseSystemCNF(string(cnf), "BOOT2")
		if !ok {
			return img, fmt.Errorf("%s: %w: SYSTEM.CNF has no BOOT2 entry", filepath.Base(name), ErrNotPS2)
		}
		img.BootFile = boot
		img.GameID = model.FindGameID(boot)
		if img.GameID == "" {
			return img, fmt.Errorf("%s: BOOT2 is %q, which carries no recognisable serial", filepath.Base(name), boot)
		}
	case partial:
		id, err := serialFromRoot(vol)
		if err != nil {
			return img, fmt.Errorf("%s: %w: %v", filepath.Base(name), ErrNotPS2, err)
		}
		img.GameID = id
	default:
		return img, fmt.Errorf("%s: %w: SYSTEM.CNF is missing", filepath.Base(name), ErrNotPS2)
	}

	// The volume space size is what hdl_dump uses to size the install; it can
	// be smaller than the file when the image is padded, and larger when the
	// file has been truncated.
	if vs := vol.SizeBytes(); vs > 0 && vs < img.SizeBytes {
		img.SizeBytes = vs
	}
	img.Media, img.MediaFromSize = MediaType(vol, img.SizeBytes)
	img.Title = TitleFromPath(name, img.VolumeID)
	return img, nil
}

// bootELFSerial returns the serial a root-directory entry names, or "".
// See model.BootFileSerial for why the strict form matters here.
func bootELFSerial(name string) string { return model.BootFileSerial(name) }

// openVolume tries the two sector layouts a PS2 rip can have.
//
// A CD-based PS2 title ripped with a CD ripper is MODE2/2352: 24 bytes of sync
// and header in front of each 2048-byte block. A DVD-based title, or a BIN
// converted to ISO, is a plain 2048 stream. Only 2048 used to be tried, which
// made every raw CD rip unidentifiable -- 54 of 513 archives in one real
// library.
//
// 2048 is tried first because it is the common case and cannot succeed by
// accident on a 2352 image: the descriptor would have to land exactly on the
// signature. The 2352 attempt is gated on the size being a whole number of
// raw sectors, which a 2048 image is not, except by coincidence.
func openVolume(r io.ReaderAt, size int64) (*iso9660.Volume, error) {
	v, err := iso9660.Open(iso9660.Mode2048(r))
	if err == nil {
		return v, nil
	}
	if size%RawSectorSize == 0 {
		if v2, err2 := iso9660.Open(iso9660.Mode2352(r)); err2 == nil {
			return v2, nil
		}
	}
	return nil, err
}

// serialFromRoot finds the boot ELF in the root directory and reads the serial
// off its name.
//
// It insists on exactly one match. A disc with two serial-shaped names at the
// root is not something to guess about, and one with none is not a PS2 disc.
func serialFromRoot(vol *iso9660.Volume) (string, error) {
	names, err := vol.ReadDir()
	if err != nil {
		return "", fmt.Errorf("SYSTEM.CNF is out of reach and the root directory is unreadable: %w", err)
	}
	var found []string
	for _, n := range names {
		if id := bootELFSerial(n); id != "" {
			found = append(found, id)
		}
	}
	switch len(found) {
	case 1:
		return found[0], nil
	case 0:
		return "", fmt.Errorf("SYSTEM.CNF is out of reach and no boot ELF named for a serial is in the root directory")
	default:
		return "", fmt.Errorf("SYSTEM.CNF is out of reach and the root directory holds %d serial-shaped names (%s)",
			len(found), strings.Join(found, ", "))
	}
}

// MediaType decides whether an image was mastered for CD or DVD, and reports
// whether it had to fall back to guessing from the size.
//
// The distinction matters: hdl_dump takes a different verb for each
// (inject_cd vs inject_dvd), and installing a DVD image as a CD produces a
// game the console will not boot.
//
// Every PlayStation CD is a CD-ROM XA disc and carries the "CD-XA001"
// signature in the volume descriptor's application area; a DVD leaves that
// area blank. That signature, not the image size, is the real answer, and it
// is what hdl_dump itself uses (isofs.c, isofs_detect_media_type). Size is
// kept only for images whose XA area holds something else entirely, which
// happens with unusual mastering tools.
func MediaType(vol *iso9660.Volume, sizeBytes int64) (media model.MediaType, guessed bool) {
	switch {
	case vol.IsCDXA():
		return model.MediaCD, false
	case vol.XAMarkerIsBlank():
		return model.MediaDVD, false
	case sizeBytes <= cdSizeLimit:
		return model.MediaCD, true
	default:
		return model.MediaDVD, true
	}
}

// ParseSystemCNF returns the value of a SYSTEM.CNF key.
//
// SYSTEM.CNF is a handful of "KEY = value" lines with CRLF endings, and real
// discs are inconsistent about spacing and case.
func ParseSystemCNF(text, key string) (string, bool) {
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r\x00"))
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if !strings.EqualFold(strings.TrimSpace(k), key) {
			continue
		}
		return strings.TrimSpace(v), true
	}
	return "", false
}

// TitleFromPath derives a display title. The image filename is what a user
// recognises, so it wins; the ISO volume id is the fallback, and it is only
// used when it is not just a restatement of the serial.
func TitleFromPath(path, volumeID string) string {
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	base = CleanTitle(base)
	if base != "" {
		return base
	}
	if v := CleanTitle(volumeID); v != "" && model.FindGameID(volumeID) == "" {
		return v
	}
	return filepath.Base(path)
}

// CleanTitle turns a filename into something readable: it drops a leading
// serial, collapses separators, and trims the region and disc tags that
// scene releases carry.
func CleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	// A leading "SLUS_209.46." or "SLUS-20946 - " prefix is redundant with the
	// serial column, so it is removed.
	if id := model.FindGameID(s); id != "" {
		if i := serialPrefixEnd(s); i > 0 {
			s = strings.TrimSpace(s[i:])
			s = strings.TrimLeft(s, " -._")
		}
	}
	s = strings.NewReplacer("_", " ", ".", " ").Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

// serialPrefixEnd reports the end of a serial that appears at the very start
// of s, or 0 when the serial is elsewhere.
func serialPrefixEnd(s string) int {
	loc := serialLocation(s)
	if loc == nil || loc[0] > 2 {
		return 0
	}
	return loc[1]
}

func serialLocation(s string) []int {
	return serialRe.FindStringIndex(s)
}

// serialRe mirrors the pattern in internal/model so that title cleaning can
// locate a serial's extent, which FindGameID does not report.
var serialRe = regexp.MustCompile(`(?i)\b([A-Z]{2,5})[ _\-]?([0-9]{3})[ .\-]?([0-9]{2})\b`)
