package external

import (
	"context"
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/casmith/ps2hdd/internal/model"
)

// HDLDumpTool is the executable name.
const HDLDumpTool = "hdl_dump"

// HDLGame is one row of `hdl_dump hdl_toc`.
type HDLGame struct {
	IsDVD       bool
	SizeKB      int64
	CompatFlags string
	DMA         string
	Startup     string
	Name        string
}

// HDLToc is the parsed output of `hdl_dump hdl_toc`.
type HDLToc struct {
	Games []HDLGame
	// Totals come from the trailing summary line, in megabytes.
	TotalMB int64
	UsedMB  int64
	FreeMB  int64
}

// HDLDump wraps the hdl_dump executable.
//
// ps2hdd reads the installed-game list natively (see internal/apa), so this
// type exists for the operations that write: installing and removing games.
// ListGames is still provided because it is the reference implementation and a
// useful cross-check in `ps2hdd doctor`.
type HDLDump struct {
	Runner Runner
}

// Available reports the resolved path to hdl_dump, if it is installed.
func (h HDLDump) Available() (string, bool) { return Available(h.Runner, HDLDumpTool) }

// ListGames runs `hdl_dump hdl_toc <device> --csv`.
func (h HDLDump) ListGames(ctx context.Context, device string) (HDLToc, error) {
	res, err := h.Runner.Run(ctx, Command{
		Name:       HDLDumpTool,
		Args:       []string{"hdl_toc", device, "--csv"},
		Privileged: true,
	})
	if err != nil {
		return HDLToc{}, err
	}
	return ParseHDLToc(res.Stdout)
}

// hdlTotalsRe matches the summary line hdl_dump prints after the game list.
var hdlTotalsRe = regexp.MustCompile(`^total (\d+)MB, used (\d+)MB, available (\d+)MB`)

// ParseHDLToc parses `hdl_dump hdl_toc --csv` output.
//
// The format is fixed by show_hdl_toc in hdl-dump's hdl_dump.c. Note that the
// column header is printed *unseparated* even in CSV mode, so it has to be
// skipped by content rather than by position; and the size column carries a
// "KB" suffix inside the field.
//
//	type      size flags       dma startup      name
//	DVD;3538944KB;  0        ;*u4;SLUS_210.50;Burnout 3
//	total 114432MB, used 8320MB, available 106112MB
func ParseHDLToc(out string) (HDLToc, error) {
	var toc HDLToc
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimRight(line, "\r")
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if m := hdlTotalsRe.FindStringSubmatch(trimmed); m != nil {
			toc.TotalMB, _ = strconv.ParseInt(m[1], 10, 64)
			toc.UsedMB, _ = strconv.ParseInt(m[2], 10, 64)
			toc.FreeMB, _ = strconv.ParseInt(m[3], 10, 64)
			continue
		}
		fields := strings.Split(line, ";")
		if len(fields) < 6 {
			continue // the header row, or hdl_dump's banner
		}
		typ := strings.TrimSpace(fields[0])
		if typ != "CD" && typ != "DVD" {
			continue
		}
		sizeKB, err := parseKB(fields[1])
		if err != nil {
			return toc, fmt.Errorf("hdl_toc: parse size %q: %w", fields[1], err)
		}
		toc.Games = append(toc.Games, HDLGame{
			IsDVD:       typ == "DVD",
			SizeKB:      sizeKB,
			CompatFlags: strings.TrimSpace(fields[2]),
			DMA:         strings.TrimSpace(fields[3]),
			Startup:     strings.TrimSpace(fields[4]),
			// The name is the last field and may itself contain semicolons,
			// so it is rejoined rather than indexed.
			Name: strings.TrimSpace(strings.Join(fields[5:], ";")),
		})
	}
	return toc, nil
}

func parseKB(s string) (int64, error) {
	s = strings.TrimSpace(s)
	s = strings.TrimSuffix(s, "KB")
	return strconv.ParseInt(strings.TrimSpace(s), 10, 64)
}

// InstallRequest describes one `hdl_dump inject_cd` / `inject_dvd` run.
type InstallRequest struct {
	Device string
	// Name is the title as it will appear in OPL and the HDD browser.
	Name string
	// Source is the path to the ISO or cuesheet.
	Source string
	// Startup is the OPL-form serial, e.g. SLUS_209.46.
	Startup string
	// Media selects inject_cd or inject_dvd.
	Media model.MediaType
	// DMA is the transfer mode, e.g. "*u4". Empty leaves hdl_dump's default.
	DMA string
	// CompatFlags is the "+1+2" form. Empty means none.
	CompatFlags string
	// Hidden hides the game from the PS2 HDD browser.
	Hidden bool
	// OnProgress receives a 0..1 fraction as hdl_dump reports it.
	OnProgress func(fraction float64, stage string)
}

// InstallArgs builds the hdl_dump argument vector for a request. It is
// exported and pure so that the exact command line can be unit tested and
// shown by --dry-run without running anything.
//
// Syntax, from hdl_dump.c:
//
//	inject_cd  target name source [startup] [+flags] [*dma] [@slice] [-hide]
//	inject_dvd target name source [startup] [+flags] [*dma] [@slice] [-hide]
func InstallArgs(req InstallRequest) ([]string, error) {
	if req.Device == "" {
		return nil, fmt.Errorf("install: no device")
	}
	if req.Name == "" {
		return nil, fmt.Errorf("install: no game name")
	}
	if req.Source == "" {
		return nil, fmt.Errorf("install: no source image")
	}
	verb := "inject_dvd"
	switch req.Media {
	case model.MediaCD:
		verb = "inject_cd"
	case model.MediaDVD:
		verb = "inject_dvd"
	default:
		return nil, fmt.Errorf("install: media type of %s is unknown; ps2hdd will not guess between a CD and a DVD image", req.Source)
	}
	args := []string{verb, req.Device, req.Name, req.Source}
	if req.Startup != "" {
		args = append(args, req.Startup)
	}
	if req.CompatFlags != "" && req.CompatFlags != "0" {
		args = append(args, req.CompatFlags)
	}
	if req.DMA != "" {
		args = append(args, req.DMA)
	}
	if req.Hidden {
		args = append(args, "-hide")
	}
	return args, nil
}

// Install injects a game image into a new HDL partition.
func (h HDLDump) Install(ctx context.Context, req InstallRequest) error {
	args, err := InstallArgs(req)
	if err != nil {
		return err
	}
	onLine := func(line string) {
		if req.OnProgress == nil {
			return
		}
		if frac, ok := ParseHDLProgress(line); ok {
			req.OnProgress(frac, "installing")
		}
	}
	_, err = h.Runner.Run(ctx, Command{
		Name:       HDLDumpTool,
		Args:       args,
		Privileged: true,
		OnStdout:   onLine,
		OnStderr:   onLine,
	})
	return err
}

// hdlProgressRe matches the percentage in hdl_dump's progress bar. The bar is
// redrawn with a carriage return, which the command runner already splits on.
//
//	[=====>       ]  42%, 00:01:23 remaining, 12.34 MB/sec
//	 42%
var hdlProgressRe = regexp.MustCompile(`(\d{1,3})%`)

// ParseHDLProgress extracts a completion fraction from one line of hdl_dump
// output. When hdl_dump reports no percentage the caller must show an
// indeterminate spinner rather than invent a number.
func ParseHDLProgress(line string) (float64, bool) {
	m := hdlProgressRe.FindStringSubmatch(line)
	if m == nil {
		return 0, false
	}
	pct, err := strconv.Atoi(m[1])
	if err != nil || pct < 0 || pct > 100 {
		return 0, false
	}
	return float64(pct) / 100, true
}

// RemoveArgs builds the argument vector for removing a game.
//
// hdl_dump's removal verb is "delete", and it takes either a partition name or
// a game name (hdl_dump.c: CMD_HIDE). ps2hdd always passes the partition name,
// which is unambiguous; matching by title is done on our side so the user can
// be shown exactly what will go.
func RemoveArgs(device, partition string) ([]string, error) {
	if device == "" {
		return nil, fmt.Errorf("remove: no device")
	}
	if partition == "" {
		return nil, fmt.Errorf("remove: no partition name")
	}
	return []string{"delete", device, partition}, nil
}

// Remove deletes an installed game's partition.
func (h HDLDump) Remove(ctx context.Context, device, partition string) error {
	args, err := RemoveArgs(device, partition)
	if err != nil {
		return err
	}
	_, err = h.Runner.Run(ctx, Command{
		Name:       HDLDumpTool,
		Args:       args,
		Privileged: true,
	})
	return err
}

// CDVDInfo is the parsed output of `hdl_dump cdvd_info2 <image> --csv`.
type CDVDInfo struct {
	MediaType model.MediaType
	SizeKB    int64
	VolumeID  string
	Startup   string
	DualLayer bool
}

// ParseCDVDInfo parses `cdvd_info2 --csv`, whose format is
// "[dual-layer ]CD|DVD;<n>KB;<volume id>;<startup>" (hdl_dump.c: cdvd_info).
func ParseCDVDInfo(out string) (CDVDInfo, error) {
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		if line == "" || !strings.Contains(line, ";") {
			continue
		}
		f := strings.Split(line, ";")
		if len(f) < 4 {
			continue
		}
		var info CDVDInfo
		typ := strings.TrimSpace(f[0])
		if rest, ok := strings.CutPrefix(typ, "dual-layer "); ok {
			info.DualLayer = true
			typ = rest
		}
		switch typ {
		case "CD":
			info.MediaType = model.MediaCD
		case "DVD":
			info.MediaType = model.MediaDVD
		default:
			continue
		}
		kb, err := parseKB(f[1])
		if err != nil {
			return info, fmt.Errorf("cdvd_info2: parse size %q: %w", f[1], err)
		}
		info.SizeKB = kb
		info.VolumeID = strings.TrimSpace(f[2])
		info.Startup = strings.TrimSpace(f[3])
		return info, nil
	}
	return CDVDInfo{}, fmt.Errorf("cdvd_info2: no recognisable output")
}

// CDVDInfo runs `hdl_dump cdvd_info2` against an image.
func (h HDLDump) CDVDInfo(ctx context.Context, image string) (CDVDInfo, error) {
	res, err := h.Runner.Run(ctx, Command{
		Name: HDLDumpTool,
		Args: []string{"cdvd_info2", image, "--csv"},
	})
	if err != nil {
		return CDVDInfo{}, err
	}
	return ParseCDVDInfo(res.Stdout)
}
