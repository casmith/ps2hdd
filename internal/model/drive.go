package model

// BlockDevice is one entry from lsblk, before any PS2-specific judgement.
type BlockDevice struct {
	Name       string        `json:"name"`       // sdb
	Path       string        `json:"path"`       // /dev/sdb
	Type       string        `json:"type"`       // disk, part, loop...
	SizeBytes  int64         `json:"size_bytes"` //
	Model      string        `json:"model"`      // WDC WD10EZEX-08WN4A0
	Serial     string        `json:"serial"`     //
	Transport  string        `json:"transport"`  // sata, usb, nvme...
	ReadOnly   bool          `json:"read_only"`  //
	Mountpoint string        `json:"mountpoint"` //
	Children   []BlockDevice `json:"children,omitempty"`
	// ByID is the stable /dev/disk/by-id path this device resolves to. Only
	// this form is ever persisted to configuration.
	ByID string `json:"by_id,omitempty"`
}

// AnyMountpoint reports the first mountpoint on the device or any of its
// partitions, which is what makes a disk unsafe to write to.
func (d BlockDevice) AnyMountpoint() string {
	if d.Mountpoint != "" {
		return d.Mountpoint
	}
	for _, c := range d.Children {
		if m := c.AnyMountpoint(); m != "" {
			return m
		}
	}
	return ""
}

// Mountpoints lists every mountpoint on the device and its partitions.
func (d BlockDevice) Mountpoints() []string {
	var out []string
	if d.Mountpoint != "" {
		out = append(out, d.Mountpoint)
	}
	for _, c := range d.Children {
		out = append(out, c.Mountpoints()...)
	}
	return out
}

// Partition is one APA partition as read from the on-disk partition table.
type Partition struct {
	ID          string `json:"id"`   // __mbr, +OPL, PP.HDL.Game, __.POPS
	Type        uint16 `json:"type"` // 0x0001 MBR, 0x0100 PFS, 0x1337 HDL
	StartSector uint32 `json:"start_sector"`
	Sectors     uint32 `json:"sectors"`     // main partition only
	TotalBytes  int64  `json:"total_bytes"` // main + all sub-partitions
	SubCount    uint32 `json:"sub_count"`
	Slice       int    `json:"slice"`
	Main        bool   `json:"main"`
}

// DriveStatus is the read-only picture of a PS2 HDD that the TUI Drive view
// and `ps2hdd status` both render.
type DriveStatus struct {
	ByID       string `json:"by_id"`
	DevicePath string `json:"device_path"`
	Model      string `json:"model"`
	Serial     string `json:"serial"`
	SizeBytes  int64  `json:"size_bytes"`

	APADetected bool        `json:"apa_detected"`
	Partitions  []Partition `json:"partitions,omitempty"`

	HasOPL    bool `json:"has_opl"`    // +OPL partition present
	HasPOPS   bool `json:"has_pops"`   // __.POPS partition present
	HasCommon bool `json:"has_common"` // __common partition present

	PS2Games int `json:"ps2_games"`
	PS1Games int `json:"ps1_games"`

	TotalBytes int64 `json:"apa_total_bytes"`
	UsedBytes  int64 `json:"apa_used_bytes"`
	FreeBytes  int64 `json:"apa_free_bytes"`

	// Notes carries non-fatal observations, e.g. that a second APA slice was
	// detected or that the free-space figure excludes a slice.
	Notes []string `json:"notes,omitempty"`
}

// FindPartition returns the partition with the given id, case-insensitively.
func (s DriveStatus) FindPartition(id string) (Partition, bool) {
	for _, p := range s.Partitions {
		if equalFold(p.ID, id) {
			return p, true
		}
	}
	return Partition{}, false
}

func equalFold(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := 0; i < len(a); i++ {
		ca, cb := a[i], b[i]
		if 'A' <= ca && ca <= 'Z' {
			ca += 'a' - 'A'
		}
		if 'A' <= cb && cb <= 'Z' {
			cb += 'a' - 'A'
		}
		if ca != cb {
			return false
		}
	}
	return true
}
