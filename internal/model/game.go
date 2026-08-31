// Package model holds the data types shared by every layer of ps2hdd.
//
// Nothing in this package performs I/O. Services in internal/drive,
// internal/catalog, internal/platform and internal/asset produce and consume
// these values, and the CLI and TUI render them.
package model

import (
	"fmt"
	"sort"
	"strings"
)

// Platform identifies the console a title targets.
type Platform string

const (
	PlatformPS1 Platform = "ps1"
	PlatformPS2 Platform = "ps2"
)

// String implements fmt.Stringer.
func (p Platform) String() string { return string(p) }

// Label is the short upper-case form used in tables.
func (p Platform) Label() string {
	switch p {
	case PlatformPS1:
		return "PS1"
	case PlatformPS2:
		return "PS2"
	default:
		return strings.ToUpper(string(p))
	}
}

// MediaType describes the physical medium a PS2 image was mastered for. It
// decides whether hdl_dump is asked to inject a CD or a DVD.
type MediaType string

const (
	MediaUnknown MediaType = ""
	MediaCD      MediaType = "cd"
	MediaDVD     MediaType = "dvd"
)

// StorageBackend names the on-HDD representation of an installed title.
const (
	// BackendHDL is an APA partition of type 0x1337 written by hdl_dump.
	BackendHDL = "hdl"
	// BackendPOPS is a .VCD file inside the __.POPS PFS partition.
	BackendPOPS = "pops"
)

// Game is one logical title. A multi-disc PS1 release is a single Game with
// several Discs; a PS2 title always has exactly one Disc.
type Game struct {
	Platform  Platform `json:"platform"`
	Title     string   `json:"title"`
	GameID    string   `json:"game_id"`
	SizeBytes int64    `json:"size_bytes"`
	// InstallSizeBytes is what the title costs on the PS2 HDD, which is not
	// its size as a file. A PS2 image is rounded up to whole 128 MiB APA
	// chunks and charged partition overhead on top; a PS1 rip gains a 1 MiB
	// POPS header and any pregap the conversion materialises. For an already
	// installed title it is the measured footprint, so it equals SizeBytes.
	//
	// Zero means "not known here" -- a source entry built without the drive
	// present, say -- and callers fall back to SizeBytes.
	InstallSizeBytes int64     `json:"install_size_bytes,omitempty"`
	StorageBackend   string    `json:"storage_backend,omitempty"`
	Media            MediaType `json:"media,omitempty"`
	Discs            []Disc    `json:"discs,omitempty"`
	Installed        bool      `json:"installed"`
	SourcePath       string    `json:"source_path,omitempty"`
	// ArchiveMember is the path inside SourcePath when the source is an
	// archive rather than a loose image. Empty for a loose image, and that
	// emptiness is what every caller tests to decide whether the source can
	// be handed straight to hdl_dump or has to be extracted first.
	ArchiveMember string `json:"archive_member,omitempty"`

	// PartitionName is the APA partition backing an installed PS2 game, or the
	// VCD file name of an installed PS1 game. Empty for source-only entries.
	PartitionName string `json:"partition_name,omitempty"`
}

// Key is the stable identity used to reconcile source and installed views.
// Game IDs are normalised so that "SLUS-209.46", "slus_20946" and
// "SLUS_209.46" all collapse to the same key.
func (g Game) Key() string {
	if id := NormalizeGameID(g.GameID); id != "" {
		return string(g.Platform) + ":" + id
	}
	return string(g.Platform) + ":title:" + strings.ToLower(strings.TrimSpace(g.Title))
}

// DiscCount reports the number of discs, treating an empty slice as one disc.
func (g Game) DiscCount() int {
	if len(g.Discs) == 0 {
		return 1
	}
	return len(g.Discs)
}

// IsMultiDisc reports whether the title spans more than one disc.
func (g Game) IsMultiDisc() bool { return len(g.Discs) > 1 }

// SortGames orders games by platform then title, case-insensitively.
func SortGames(games []Game) {
	sort.SliceStable(games, func(i, j int) bool {
		if games[i].Platform != games[j].Platform {
			return games[i].Platform < games[j].Platform
		}
		a := strings.ToLower(games[i].Title)
		b := strings.ToLower(games[j].Title)
		if a != b {
			return a < b
		}
		return games[i].GameID < games[j].GameID
	})
}

// HumanSize renders a byte count using binary units.
func HumanSize(b int64) string {
	const unit = 1024
	if b < 0 {
		return "?"
	}
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := int64(unit), 0
	for n := b / unit; n >= unit && exp < 4; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(b)/float64(div), "KMGTP"[exp])
}

// InstallSize is what the title will cost on the HDD, falling back to the
// source size when the footprint was not worked out.
//
// Callers that are about to write to the drive should prefer a figure computed
// against that drive's actual partition table; this is the best answer
// available without one.
func (g Game) InstallSize() int64 {
	if g.InstallSizeBytes > 0 {
		return g.InstallSizeBytes
	}
	return g.SizeBytes
}
