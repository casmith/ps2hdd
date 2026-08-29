package drive

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// byIDDir holds the udev-maintained stable symlinks. Only names from here (or
// the sibling by-uuid/by-path directories) are ever written to the config
// file: /dev/sdX is reassigned across boots and hotplugs.
const byIDDir = "/dev/disk/by-id"

// ByIDPaths returns every /dev/disk/by-id entry that resolves to devPath,
// sorted so that the most descriptive name comes first.
//
// udev usually creates several links for one disk — ata-MODEL_SERIAL,
// wwn-0x..., scsi-... — and they are not equally useful to a human reading a
// config file. A wwn link is stable but opaque; an ata- or nvme- link names
// the model and serial, which is what a user needs in order to recognise the
// disk they meant.
func ByIDPaths(devPath string) ([]string, error) {
	target, err := filepath.EvalSymlinks(devPath)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", devPath, err)
	}
	entries, err := os.ReadDir(byIDDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read %s: %w", byIDDir, err)
	}
	var out []string
	for _, e := range entries {
		link := filepath.Join(byIDDir, e.Name())
		resolved, err := filepath.EvalSymlinks(link)
		if err != nil || resolved != target {
			continue
		}
		out = append(out, link)
	}
	sort.Slice(out, func(i, j int) bool {
		pi, pj := byIDPriority(out[i]), byIDPriority(out[j])
		if pi != pj {
			return pi < pj
		}
		return out[i] < out[j]
	})
	return out, nil
}

func byIDPriority(p string) int {
	base := filepath.Base(p)
	switch {
	case strings.HasPrefix(base, "ata-"), strings.HasPrefix(base, "nvme-"):
		return 0
	case strings.HasPrefix(base, "usb-"), strings.HasPrefix(base, "scsi-"):
		return 1
	case strings.HasPrefix(base, "wwn-"):
		return 3
	default:
		return 2
	}
}

// PreferredByID returns the best stable identifier for a device, or "" when
// udev has created none (which happens for loop devices and disk images).
func PreferredByID(devPath string) string {
	paths, err := ByIDPaths(devPath)
	if err != nil || len(paths) == 0 {
		return ""
	}
	return paths[0]
}

// Resolve turns a configured identifier into the kernel device it currently
// points at. A regular file is returned unchanged: working from a disk image
// is how the test suite and anyone recovering from a dump address a HDD.
func Resolve(dev string) (string, error) {
	fi, err := os.Stat(dev)
	if err != nil {
		return "", fmt.Errorf("device %s is not present: %w", dev, err)
	}
	if fi.Mode().IsRegular() {
		return filepath.Abs(dev)
	}
	resolved, err := filepath.EvalSymlinks(dev)
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", dev, err)
	}
	return resolved, nil
}

// mountinfoPath is the kernel's per-process mount table.
const mountinfoPath = "/proc/self/mountinfo"

// SystemDevices returns the set of resolved device paths that back mounted
// filesystems, mapped to the mountpoints that use them.
//
// The check that matters most is that / and /boot are never on the target
// disk; the broader map is used to refuse any disk carrying a mounted Linux
// filesystem, which a PS2 HDD never does.
func SystemDevices() (map[string][]string, error) {
	f, err := os.Open(mountinfoPath)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", mountinfoPath, err)
	}
	defer f.Close()
	return parseMountinfo(f)
}

func parseMountinfo(r interface{ Read([]byte) (int, error) }) (map[string][]string, error) {
	out := map[string][]string{}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		// mountinfo: id parent major:minor root mountpoint options... - fstype source superopts
		line := sc.Text()
		sep := strings.Index(line, " - ")
		if sep < 0 {
			continue
		}
		left := strings.Fields(line[:sep])
		right := strings.Fields(line[sep+3:])
		if len(left) < 5 || len(right) < 2 {
			continue
		}
		mountpoint := unescapeMountinfo(left[4])
		source := unescapeMountinfo(right[1])
		if !strings.HasPrefix(source, "/dev/") {
			continue
		}
		resolved, err := filepath.EvalSymlinks(source)
		if err != nil {
			resolved = source
		}
		out[resolved] = append(out[resolved], mountpoint)
	}
	return out, sc.Err()
}

// unescapeMountinfo decodes the octal escapes the kernel uses for spaces,
// tabs, newlines and backslashes in mountinfo fields.
func unescapeMountinfo(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == '\\' && i+3 < len(s) {
			v := 0
			ok := true
			for j := 1; j <= 3; j++ {
				c := s[i+j]
				if c < '0' || c > '7' {
					ok = false
					break
				}
				v = v*8 + int(c-'0')
			}
			if ok {
				b.WriteByte(byte(v))
				i += 4
				continue
			}
		}
		b.WriteByte(s[i])
		i++
	}
	return b.String()
}

// DeviceSize reports the size in bytes of a block device or a regular file.
func DeviceSize(path string) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return 0, err
	}
	if fi.Mode().IsRegular() {
		return fi.Size(), nil
	}
	// Block devices report size 0 from stat; seeking to the end is the
	// portable way to measure one without an ioctl.
	return f.Seek(0, 2)
}
