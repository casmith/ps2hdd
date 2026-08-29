package drive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/casmith/ps2hdd/internal/apa"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
	"github.com/casmith/ps2hdd/internal/model"
)

// Candidate is a disk that `ps2hdd detect` found and classified.
type Candidate struct {
	Device model.BlockDevice `json:"device"`
	// ByID is the stable identifier to persist, or "" when udev created none.
	ByID string `json:"by_id"`
	// APA is true when a valid APA table was read from the disk.
	APA bool `json:"apa"`
	// Skipped explains why a disk was not considered a candidate. An empty
	// value means it is one.
	Skipped string `json:"skipped,omitempty"`
	// ReadError records why the APA probe could not run, most often because
	// the raw device is root-owned.
	ReadError string `json:"read_error,omitempty"`
}

// IsCandidate reports whether the disk looks like a usable PS2 HDD.
func (c Candidate) IsCandidate() bool { return c.Skipped == "" && c.APA }

// Detect enumerates whole disks and probes each for an APA partition table.
//
// Detection is strictly read-only: it opens devices O_RDONLY, never writes,
// and never initialises anything. Disks that back mounted filesystems are
// reported as skipped rather than probed, so a run of `ps2hdd detect` on a
// laptop cannot be misread as an invitation to touch the system disk.
func Detect(ctx context.Context, r external.Runner) ([]Candidate, error) {
	log := logging.ContextLogger(ctx)
	devs, err := ListBlockDevices(ctx, r)
	if err != nil {
		return nil, fmt.Errorf("enumerate block devices: %w", err)
	}
	mounts, mountErr := SystemDevices()

	var out []Candidate
	for _, d := range devs {
		if d.Type != "disk" {
			continue
		}
		c := Candidate{Device: d, ByID: PreferredByID(d.Path)}
		c.Device.ByID = c.ByID

		switch {
		case mountErr != nil:
			c.Skipped = "the mount table could not be read, so system-disk status is unknown"
		case len(mountedOn(mounts, d.Path)) > 0:
			c.Skipped = "carries mounted Linux filesystems (" +
				strings.Join(mountedOn(mounts, d.Path), ", ") + ")"
		case d.SizeBytes == 0:
			c.Skipped = "reports a capacity of zero bytes"
		}
		if c.Skipped == "" {
			apaOK, err := probeAPA(d.Path)
			switch {
			case err != nil:
				c.ReadError = err.Error()
			default:
				c.APA = apaOK
			}
		}
		log.Debug("detect probed disk", "path", d.Path, "apa", c.APA,
			"skipped", c.Skipped, "read_error", c.ReadError)
		out = append(out, c)
	}
	return out, nil
}

func mountedOn(mounts map[string][]string, path string) []string {
	var out []string
	for src, points := range mounts {
		if sameDisk(src, path) {
			out = append(out, points...)
		}
	}
	return out
}

func probeAPA(path string) (bool, error) {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		if errors.Is(err, os.ErrPermission) {
			return false, fmt.Errorf("permission denied reading %s (run with sudo, or install the udev rule in docs/safety.md)", path)
		}
		return false, err
	}
	defer f.Close()
	return apa.IsAPA(f)
}
