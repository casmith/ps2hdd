package asset

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

// CheckWritable confirms the mounted partition accepts a new file.
//
// It exists because the alternative is a wall of identical failures, one per
// artwork file, each reporting a raw errno against a different path. A mount
// that cannot take a file cannot take any of them, and saying so once -- with
// what the errno actually means for a PFS mount over FUSE -- is the difference
// between a diagnosis and a log to scroll through.
//
// The probe writes a byte and removes it. A partition that fails here is left
// exactly as it was found.
func CheckWritable(mountpoint string) error {
	probe := filepath.Join(mountpoint, ".ps2hdd-write-probe")
	// Never O_TRUNC: pfsfuse has no truncate, so a probe left behind by an
	// interrupted run would make this report the mount as unwritable when it
	// is not. See the note in Manager.install.
	if err := os.Remove(probe); err != nil && !errors.Is(err, os.ErrNotExist) {
		return writeProbeError(mountpoint, err)
	}
	f, err := os.OpenFile(probe, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return writeProbeError(mountpoint, err)
	}
	_, werr := f.Write([]byte{0})
	cerr := f.Close()
	_ = os.Remove(probe)
	if werr != nil {
		return writeProbeError(mountpoint, werr)
	}
	if cerr != nil {
		return writeProbeError(mountpoint, cerr)
	}
	return nil
}

// writeProbeError explains a failed probe in terms of the thing that actually
// went wrong, because the raw errno from a FUSE mount is close to meaningless
// on its own.
func writeProbeError(mountpoint string, err error) error {
	hint := ""
	switch {
	case errors.Is(err, syscall.ENOSYS):
		hint = "The mount reported the operation as not implemented, which means pfsfuse served the read side but not the write side. " +
			"Check that pfsfuse is a current build, and that the partition really holds a PFS filesystem rather than being an empty APA partition."
	case errors.Is(err, syscall.EROFS):
		hint = "The mount is read-only."
	case errors.Is(err, syscall.EACCES), errors.Is(err, syscall.EPERM):
		hint = "Permission was refused. pfsfuse mounts are private to the user that created them unless -o allow_other is set, " +
			"so this happens when the mount and the write come from different users."
	case errors.Is(err, syscall.ENOSPC):
		hint = "The partition is full."
	}
	msg := fmt.Sprintf("%s cannot be written to: %v", mountpoint, err)
	if hint != "" {
		msg += "\n" + hint
	}
	return fmt.Errorf("%s\nNo artwork was installed and nothing on the disk was changed", msg)
}
