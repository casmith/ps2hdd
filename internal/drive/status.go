package drive

import (
	"context"
	"fmt"
	"os"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/model"
)

// Partition ids a FreeHDBoot/OPL installation is expected to have.
const (
	PartitionOPL    = "+OPL"
	PartitionPOPS   = "__.POPS"
	PartitionCommon = "__common"
)

// Status reads the drive overview: geometry, APA layout, partitions and the
// installed HDLoader game count. It is read-only and needs no external tools.
func Status(ctx context.Context, t *Target) (model.DriveStatus, error) {
	s := model.DriveStatus{
		ByID:       t.Configured,
		DevicePath: t.Path,
		Model:      t.Model,
		Serial:     t.Serial,
		SizeBytes:  t.SizeBytes,
	}
	f, err := os.Open(t.Path)
	if err != nil {
		return s, fmt.Errorf("open %s: %w", t.Path, err)
	}
	defer f.Close()

	toc, err := apa.ReadTOC(f, t.SizeBytes)
	if err != nil {
		return s, err
	}
	s.APADetected = true

	for _, sl := range toc.Slices {
		for _, p := range sl.Partitions {
			if !p.IsMain() {
				continue
			}
			s.Partitions = append(s.Partitions, model.Partition{
				ID:          p.ID,
				Type:        p.Type,
				StartSector: p.Start,
				Sectors:     p.Length,
				TotalBytes:  int64(p.TotalSectors()) * apa.SectorSize,
				SubCount:    p.NSub,
				Slice:       sl.Index,
				Main:        true,
			})
		}
	}
	_, s.HasOPL = findPart(s.Partitions, PartitionOPL)
	_, s.HasPOPS = findPart(s.Partitions, PartitionPOPS)
	_, s.HasCommon = findPart(s.Partitions, PartitionCommon)

	total, used, free := toc.Chunks()
	const chunkBytes = int64(apa.ChunkMB) * 1024 * 1024
	s.TotalBytes = int64(total) * chunkBytes
	s.UsedBytes = int64(used) * chunkBytes
	s.FreeBytes = int64(free) * chunkBytes

	games, err := apa.ReadGames(f, toc)
	if err != nil {
		return s, err
	}
	s.PS2Games = len(games)

	if toc.Got2ndSlice {
		s.Notes = append(s.Notes, "This drive uses the two-slice APA extension for capacities above 128 GB.")
	} else if toc.Is2Slice {
		s.Notes = append(s.Notes, "The partition table is marked as two-slice but the drive is not large enough for a second slice.")
	}
	return s, nil
}

func findPart(parts []model.Partition, id string) (model.Partition, bool) {
	for _, p := range parts {
		if equalFoldASCII(p.ID, id) {
			return p, true
		}
	}
	return model.Partition{}, false
}

func equalFoldASCII(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 32
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 32
		}
		if ca != cb {
			return false
		}
	}
	return true
}
