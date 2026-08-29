// Package drive discovers block devices, resolves them to stable identifiers,
// validates that a device is safe to write to, and manages PFS mounts.
package drive

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/model"
)

// lsblkColumns is the column set requested from lsblk. PKNAME is included so a
// partition can be attributed to its parent disk when walking mountinfo.
const lsblkColumns = "NAME,PATH,TYPE,SIZE,MODEL,SERIAL,TRAN,RO,MOUNTPOINT,PKNAME"

// lsblkOutput mirrors `lsblk --json`. Numeric columns come back as JSON
// numbers with --bytes but as strings without it, and the `ro` column is a
// bool on some versions and "0"/"1" on others, so both shapes are accepted.
type lsblkOutput struct {
	BlockDevices []lsblkDevice `json:"blockdevices"`
}

type lsblkDevice struct {
	Name       string        `json:"name"`
	Path       string        `json:"path"`
	Type       string        `json:"type"`
	Size       json.Number   `json:"size"`
	Model      *string       `json:"model"`
	Serial     *string       `json:"serial"`
	Tran       *string       `json:"tran"`
	RO         any           `json:"ro"`
	Mountpoint *string       `json:"mountpoint"`
	PKName     *string       `json:"pkname"`
	Children   []lsblkDevice `json:"children"`
}

// ListBlockDevices runs lsblk and returns the disk-level devices it reports.
func ListBlockDevices(ctx context.Context, r external.Runner) ([]model.BlockDevice, error) {
	res, err := r.Run(ctx, external.Command{
		Name: "lsblk",
		Args: []string{"--json", "--bytes", "--paths", "--output", lsblkColumns},
	})
	if err != nil {
		return nil, err
	}
	return ParseLsblk([]byte(res.Stdout))
}

// ParseLsblk decodes `lsblk --json --bytes --paths` output. It is separated
// from the exec call so it can be tested against captured fixtures.
func ParseLsblk(data []byte) ([]model.BlockDevice, error) {
	var out lsblkOutput
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("parse lsblk output: %w", err)
	}
	devs := make([]model.BlockDevice, 0, len(out.BlockDevices))
	for _, d := range out.BlockDevices {
		devs = append(devs, convertLsblk(d))
	}
	return devs, nil
}

func convertLsblk(d lsblkDevice) model.BlockDevice {
	out := model.BlockDevice{
		Name:       d.Name,
		Path:       d.Path,
		Type:       d.Type,
		SizeBytes:  parseSize(d.Size),
		Model:      deref(d.Model),
		Serial:     deref(d.Serial),
		Transport:  deref(d.Tran),
		ReadOnly:   parseBoolish(d.RO),
		Mountpoint: deref(d.Mountpoint),
	}
	if out.Path == "" && out.Name != "" {
		out.Path = "/dev/" + out.Name
	}
	for _, c := range d.Children {
		out.Children = append(out.Children, convertLsblk(c))
	}
	return out
}

func parseSize(n json.Number) int64 {
	if n == "" {
		return 0
	}
	v, err := strconv.ParseInt(n.String(), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

func parseBoolish(v any) bool {
	switch t := v.(type) {
	case bool:
		return t
	case string:
		return t == "1" || t == "true"
	case float64:
		return t != 0
	case json.Number:
		return t.String() != "0"
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
