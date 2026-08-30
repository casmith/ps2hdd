package external

import (
	"context"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"
)

// SevenZipTool is the archive tool. p7zip installs it as "7z"; the "7za"
// build handles 7z only and is used as a fallback.
const (
	SevenZipTool     = "7z"
	SevenZipAltTool  = "7za"
	sevenZipListArgs = "-slt"
)

// archiveExtensions are the container formats 7z can open. Nothing here is a
// disc image: these hold one.
var archiveExtensions = map[string]bool{".7z": true, ".zip": true, ".rar": true}

// IsArchive reports whether a path names a supported archive.
//
// Extension only. A NAS full of game rips also holds sidecar files -- Synology
// writes "<name>.7z@SynoEAStream" beside every archive -- and those must not
// be opened just because their name contains ".7z".
func IsArchive(path string) bool {
	return archiveExtensions[strings.ToLower(filepath.Ext(path))]
}

// ArchiveEntry is one file inside an archive.
type ArchiveEntry struct {
	// Name is the path inside the archive, as 7z reports it.
	Name string
	// SizeBytes is the uncompressed size.
	SizeBytes int64
}

// Archive lists, streams and extracts source images held in archives.
//
// Everything goes through the 7z executable rather than a Go archive library.
// The formats that matter here are 7z, zip and rar; only the first has a
// usable pure-Go reader, and a rip library is exactly where the odd format
// turns up. Shelling out is also what the rest of this package does for
// hdl_dump and pfsfuse, for the same reason: the reference implementation
// already handles the cases a reimplementation would get wrong.
type Archive struct {
	Runner Runner
}

// Tool resolves the archive executable, preferring 7z over 7za.
func (a Archive) Tool() (string, error) {
	if _, err := a.Runner.Look(SevenZipTool); err == nil {
		return SevenZipTool, nil
	}
	if _, err := a.Runner.Look(SevenZipAltTool); err == nil {
		return SevenZipAltTool, nil
	}
	return "", fmt.Errorf("%w: %s", ErrToolMissing, SevenZipTool)
}

// Available reports whether an archive tool is installed.
func (a Archive) Available() (string, bool) {
	name, err := a.Tool()
	if err != nil {
		return "", false
	}
	p, err := a.Runner.Look(name)
	return p, err == nil
}

// ListArgs builds the argument vector that lists an archive's contents.
func ListArgs(archive string) []string {
	return []string{"l", sevenZipListArgs, archive}
}

// List returns the files inside an archive.
func (a Archive) List(ctx context.Context, archive string) ([]ArchiveEntry, error) {
	tool, err := a.Tool()
	if err != nil {
		return nil, err
	}
	res, err := a.Runner.Run(ctx, Command{Name: tool, Args: ListArgs(archive)})
	if err != nil {
		return nil, err
	}
	return ParseSevenZipList(res.Stdout), nil
}

// ParseSevenZipList parses `7z l -slt` output.
//
// The -slt format is one "Key = Value" block per entry, blocks separated by
// blank lines, after a "----------" divider. Directories are reported with an
// Attributes field containing D and are skipped: only files can be a disc
// image.
func ParseSevenZipList(out string) []ArchiveEntry {
	var (
		entries []ArchiveEntry
		cur     ArchiveEntry
		isDir   bool
		open    bool
		started bool
	)
	flush := func() {
		if open && !isDir && cur.Name != "" {
			entries = append(entries, cur)
		}
		cur, isDir, open = ArchiveEntry{}, false, false
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "----------") {
			started = true
			continue
		}
		if !started {
			continue
		}
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		key, value, ok := strings.Cut(line, " = ")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "Path":
			flush()
			cur.Name = value
			open = true
		case "Size":
			if n, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64); err == nil {
				cur.SizeBytes = n
			}
		case "Attributes":
			if strings.Contains(value, "D") {
				isDir = true
			}
		}
	}
	flush()
	return entries
}

// StreamArgs builds the argument vector that writes one member to stdout.
//
// -spd disables wildcard matching, so the member name is taken literally. The
// name came from 7z's own listing, so it needs no interpretation, and a rip
// named "Game [SLUS-20712].iso" should never depend on how a pattern matcher
// reads its brackets.
func StreamArgs(archive, inner string) []string {
	return []string{"e", "-so", "-spd", archive, inner}
}

// Stream hands fn a reader over one file inside the archive.
//
// fn is free to stop early, and normally does: identifying a game needs the
// first few megabytes of a 4 GB image, not all of it.
func (a Archive) Stream(ctx context.Context, archive, inner string, fn func(io.Reader) error) error {
	tool, err := a.Tool()
	if err != nil {
		return err
	}
	return a.Runner.Stream(ctx, Command{Name: tool, Args: StreamArgs(archive, inner)}, fn)
}

// ExtractArgs builds the argument vector that extracts one member into a
// directory.
//
// -y answers the overwrite prompt, which would otherwise wait forever on a
// stdin nothing is attached to. -spd takes the member name literally; see
// StreamArgs.
func ExtractArgs(archive, inner, destDir string) []string {
	return []string{"e", "-y", "-spd", "-o" + destDir, archive, inner}
}

// Extract writes one file from the archive into destDir and returns its path.
//
// 7z's `e` flattens any directory structure, so the result is destDir joined
// with the member's base name.
func (a Archive) Extract(ctx context.Context, archive, inner, destDir string) (string, error) {
	tool, err := a.Tool()
	if err != nil {
		return "", err
	}
	if _, err := a.Runner.Run(ctx, Command{
		Name: tool,
		Args: ExtractArgs(archive, inner, destDir),
	}); err != nil {
		return "", err
	}
	return filepath.Join(destDir, filepath.Base(inner)), nil
}

// ExtractAllArgs builds the argument vector that extracts every member into a
// directory, flattening any structure.
func ExtractAllArgs(archive, destDir string) []string {
	return []string{"e", "-y", "-o" + destDir, archive}
}

// ExtractAll writes every file in the archive into destDir.
//
// A PS1 rip is a cuesheet plus one or more data tracks, and the cuesheet names
// its track by bare filename, so they have to land together. Pulling members
// out one at a time would mean parsing the sheet first to learn what to ask
// for, which is more moving parts for no gain: these archives hold a rip and
// nothing else.
func (a Archive) ExtractAll(ctx context.Context, archive, destDir string) error {
	tool, err := a.Tool()
	if err != nil {
		return err
	}
	_, err = a.Runner.Run(ctx, Command{Name: tool, Args: ExtractAllArgs(archive, destDir)})
	return err
}
