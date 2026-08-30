package catalog

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"path/filepath"
	"sort"
	"strings"

	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/model"
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
	g.SourcePath = archivePath
	g.ArchiveMember = inner.Name
	for i := range g.Discs {
		g.Discs[i].SourcePath = archivePath
		g.Discs[i].ArchiveMember = inner.Name
	}
	return g, loc, nil
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
