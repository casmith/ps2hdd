package catalog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"
	"unicode"

	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/model"
	"github.com/casmith/ps2hdd/internal/platform/ps1"
	"github.com/casmith/ps2hdd/internal/platform/ps2"
)

// headBytes is how much of a compressed image is decompressed to identify it.
//
// Everything identification needs lives at the front of an ISO: the primary
// volume descriptor at sector 16, and the root directory it points at. 16 MiB
// covers both on every image sampled, costs a fraction of a second, and is
// small enough that several scans can run at once without the memory adding
// up. What it does not always reach is SYSTEM.CNF, whose contents are often
// gigabytes in -- see ps2.InspectAt, which handles that.
const headBytes = 16 << 20

// imageExtensions are the disc images worth looking for inside an archive.
var imageExtensions = map[string]bool{".iso": true, ".bin": true, ".img": true}

// ArchiveImage names one disc image inside an archive.
type ArchiveImage struct {
	// Archive is the path to the container.
	Archive string
	// Inner is the member's path within it.
	Inner string
	// SizeBytes is the member's uncompressed size.
	SizeBytes int64
}

// String renders the pair the way it is shown to a user.
func (a ArchiveImage) String() string { return a.Archive + "!" + a.Inner }

// FindImage picks the disc image inside an archive.
//
// An archive holding no image, or more than one, is an error rather than a
// guess: a rip library uses one archive per disc, and anything else is a
// packaging decision this cannot safely interpret.
func FindImage(entries []external.ArchiveEntry) (external.ArchiveEntry, error) {
	var images []external.ArchiveEntry
	for _, e := range entries {
		if imageExtensions[strings.ToLower(filepath.Ext(e.Name))] {
			images = append(images, e)
		}
	}
	switch len(images) {
	case 1:
		return images[0], nil
	case 0:
		return external.ArchiveEntry{}, fmt.Errorf("holds no disc image (looked for %s)", extensionList(imageExtensions))
	default:
		names := make([]string, 0, len(images))
		for _, i := range images {
			names = append(names, i.Name)
		}
		return external.ArchiveEntry{}, fmt.Errorf("holds %d disc images (%s); ps2hdd expects one archive per disc",
			len(images), strings.Join(names, ", "))
	}
}

// InspectArchivedPS2 identifies the PS2 image inside an archive without
// extracting it.
//
// Only the first headBytes of the image are decompressed. On a 4 GB rip that
// is the difference between a scan that finishes and one that does not: a
// library of five hundred archives would otherwise mean decompressing
// terabytes to build a list.
func InspectArchivedPS2(ctx context.Context, a external.Archive, archivePath string) (model.Game, ArchiveImage, error) {
	entries, err := a.List(ctx, archivePath)
	if err != nil {
		return model.Game{}, ArchiveImage{}, err
	}
	inner, err := FindImage(entries)
	if err != nil {
		return model.Game{}, ArchiveImage{}, fmt.Errorf("%s %w", filepath.Base(archivePath), err)
	}
	loc := ArchiveImage{Archive: archivePath, Inner: inner.Name, SizeBytes: inner.SizeBytes}

	head, err := readHead(ctx, a, archivePath, inner.Name)
	if err != nil {
		return model.Game{}, loc, err
	}

	// The name carried into Inspect is the archive member, so the display
	// title comes from the image's own filename rather than the container's.
	img, err := ps2.InspectAt(bytes.NewReader(head), inner.SizeBytes, inner.Name, true)
	if err != nil {
		return model.Game{}, loc, err
	}

	g := img.Game()
	g.Title = titleForArchive(g.Title, archivePath)
	g.SourcePath = archivePath
	g.ArchiveMember = inner.Name
	for i := range g.Discs {
		g.Discs[i].SourcePath = archivePath
		g.Discs[i].ArchiveMember = inner.Name
	}
	return g, loc, nil
}

// titleForArchive falls back to the archive's own name when the member's does
// not yield a title.
//
// The member is normally the better source -- it is the image's own name, and
// in most libraries it matches the archive anyway -- but not every archive is
// named that way. A member called "SLUS-20152 (1.00).iso" leaves nothing once
// the serial is taken out of it, and the game then reaches the console called
// "(1 00)". The container's name is what the user chose, so it is the sensible
// second answer.
func titleForArchive(fromMember, archivePath string) string {
	for _, r := range fromMember {
		if unicode.IsLetter(r) {
			return fromMember
		}
	}
	base := filepath.Base(archivePath)
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// readHead decompresses the start of an archive member.
//
// io.ReadFull is not used: a member shorter than headBytes is perfectly
// normal, and a short read is the expected outcome rather than a failure.
func readHead(ctx context.Context, a external.Archive, archivePath, inner string) ([]byte, error) {
	var head []byte
	err := a.Stream(ctx, archivePath, inner, func(r io.Reader) error {
		buf, err := io.ReadAll(io.LimitReader(r, headBytes))
		if err != nil {
			return err
		}
		head = buf
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read the start of %s: %w", inner, err)
	}
	if len(head) < 34*2048 {
		// Not even enough for the volume descriptor and the root directory.
		return nil, fmt.Errorf("%s yielded only %d bytes; the archive may be corrupt", inner, len(head))
	}
	return head, nil
}

func extensionList(m map[string]bool) string {
	out := make([]string, 0, len(m))
	for e := range m {
		out = append(out, e)
	}
	sort.Strings(out)
	return strings.Join(out, ", ")
}

// cueExtension is the PS1 entry point inside an archive.
const cueExtension = ".cue"

// PS1Members names the files that make up a PS1 rip inside an archive.
type PS1Members struct {
	// Cue is the cuesheet, or "" for a rip that ships without one.
	Cue string
	// Data is the track the volume descriptor lives in: the single BIN, or
	// the first of a multi-track set.
	Data external.ArchiveEntry
	// DataCount is how many data files the archive holds. More than one is a
	// split dump, which the converter joins as it goes.
	DataCount int
	// TotalBytes is every data file together, which is what the rip weighs.
	// Data.SizeBytes alone is only track 1, and for a split dump that can be a
	// small fraction of the disc.
	TotalBytes int64
}

// FindPS1Members picks the cuesheet and data track out of an archive listing.
//
// A PS1 rip is not one file the way a PS2 rip is: it is a cuesheet plus one
// or more BINs, so FindImage's insistence on exactly one image is wrong here.
func FindPS1Members(entries []external.ArchiveEntry) (PS1Members, error) {
	var m PS1Members
	var data []external.ArchiveEntry
	archives := 0
	for _, e := range entries {
		ext := strings.ToLower(filepath.Ext(e.Name))
		switch {
		case ext == cueExtension:
			if m.Cue == "" {
				m.Cue = e.Name
			}
		case imageExtensions[ext]:
			data = append(data, e)
		case external.IsArchive(e.Name):
			archives++
		}
	}
	// An archive of archives is a packaging style this does not unpack: the
	// multi-part RAR sets in some collections nest a whole split archive
	// inside an outer one. Saying so beats reporting "no disc image", which
	// is true but sends the reader looking for the wrong thing.
	if len(data) == 0 && archives > 0 {
		return m, fmt.Errorf("holds %d nested archive(s) rather than a disc image; unpack the outer archive first", archives)
	}
	if len(data) == 0 {
		return m, fmt.Errorf("holds no disc image (looked for %s)", extensionList(imageExtensions))
	}
	// Sorted so a multi-track set yields track 1 rather than whichever the
	// archive happened to list first.
	sort.Slice(data, func(i, j int) bool { return data[i].Name < data[j].Name })
	m.Data = data[0]
	m.DataCount = len(data)
	for _, e := range data {
		m.TotalBytes += e.SizeBytes
	}
	return m, nil
}

// InspectArchivedPS1 identifies the PS1 rip inside an archive without
// extracting it.
func InspectArchivedPS1(ctx context.Context, a external.Archive, archivePath string) (model.Game, error) {
	entries, err := a.List(ctx, archivePath)
	if err != nil {
		return model.Game{}, err
	}
	m, err := FindPS1Members(entries)
	if err != nil {
		return model.Game{}, fmt.Errorf("%s %w", filepath.Base(archivePath), err)
	}

	// The cuesheet is a few hundred bytes and decides whether the rip is
	// usable at all, so it is read in full.
	var cueText string
	if m.Cue != "" {
		b, err := readMember(ctx, a, archivePath, m.Cue, 1<<20)
		if err != nil {
			return model.Game{}, err
		}
		cueText = string(b)
	} else if m.DataCount > 1 {
		return model.Game{}, fmt.Errorf("%s holds %d data tracks and no cuesheet, so the track order is unknown",
			filepath.Base(archivePath), m.DataCount)
	}

	// The cuesheet decides usability, and checking it first is what keeps a
	// large library scannable: a split dump is rejected here for a few hundred
	// bytes instead of after decompressing megabytes of a track that was
	// never going to be installable. InspectReader checks it again, which
	// costs nothing and keeps it correct when called directly.
	if cueText != "" {
		c, err := ps1.ParseCue(strings.NewReader(cueText))
		if err != nil {
			return model.Game{}, fmt.Errorf("%s: %w", filepath.Base(archivePath), err)
		}
		if err := c.Validate(); err != nil {
			return model.Game{}, fmt.Errorf("%s: %w", filepath.Base(archivePath), err)
		}
	}

	head, err := readMember(ctx, a, archivePath, m.Data.Name, headBytes)
	if err != nil {
		return model.Game{}, err
	}
	d, err := ps1.InspectReader(cueText, m.Data.Name, bytes.NewReader(head), m.Data.SizeBytes, m.TotalBytes)
	if err != nil {
		return model.Game{}, err
	}

	// The archive is the source; the cuesheet is the member to hand on,
	// because that is what the converter reads.
	member := m.Cue
	if member == "" {
		member = m.Data.Name
	}
	g := model.Game{
		Platform:         model.PlatformPS1,
		Title:            titleForArchive(d.Title, archivePath),
		GameID:           d.GameID,
		SizeBytes:        d.SizeBytes,
		InstallSizeBytes: d.VCDBytes,
		SourcePath:       archivePath,
		ArchiveMember:    member,
		Discs: []model.Disc{{
			Number:           d.DiscNumber,
			GameID:           d.GameID,
			Title:            d.Title,
			SourcePath:       archivePath,
			ArchiveMember:    member,
			SizeBytes:        d.SizeBytes,
			InstallSizeBytes: d.VCDBytes,
		}},
	}
	return g, nil
}

// readMember decompresses at most limit bytes of one archive member.
func readMember(ctx context.Context, a external.Archive, archivePath, member string, limit int64) ([]byte, error) {
	var out []byte
	err := a.Stream(ctx, archivePath, member, func(r io.Reader) error {
		b, err := io.ReadAll(io.LimitReader(r, limit))
		if err != nil {
			return err
		}
		out = b
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s from %s: %w", member, filepath.Base(archivePath), err)
	}
	return out, nil
}
