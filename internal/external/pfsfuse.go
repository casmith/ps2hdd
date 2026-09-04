package external

import (
	"context"
	"fmt"
	"os"
	"strings"
)

// PFS tool names.
const (
	PFSFuseTool     = "pfsfuse"
	PFSShellTool    = "pfsshell"
	FusermountTool  = "fusermount3"
	FusermountLegcy = "fusermount"
)

// PFS mounts and unmounts PFS partitions through pfsfuse.
//
// Syntax, from the pfsshell README:
//
//	pfsfuse --partition=+OPL /path/to/device /path/to/mountpoint
//	fusermount3 -u /path/to/mountpoint
//
// pfsfuse daemonises on success, so mounting is a short-lived command whose
// completion means "mounted", not "finished".
type PFS struct {
	Runner Runner
	// AllowOther adds -o allow_other, which pfsfuse's documentation recommends
	// for full access without root. It is off by default: allow_other exposes
	// the mount to every user on the machine.
	AllowOther bool
}

// Available reports the resolved path to pfsfuse, if it is installed.
func (p PFS) Available() (string, bool) { return Available(p.Runner, PFSFuseTool) }

// singleThreaded forces FUSE to serve one request at a time.
//
// pfsfuse wraps the ps2sdk PFS and APA drivers, which are IOP code: one global
// mount state and one shared pool of buffers, written on the assumption that
// nothing else is running. Its own banner says so -- "Max mount: 1, Max open:
// 32, Number of buffers: 127". FUSE dispatches reads in parallel by default
// and calls straight into that, and the buffers are handed to whichever
// request arrives next.
//
// The symptom is silent: reads succeed and return the wrong bytes. Hashing one
// 748 MB file four times over a default mount gave three different answers, one
// of which was correct; the same file under -s hashed identically every time
// and matched a known-good copy byte for byte. The disk was never at fault --
// reading the same range of the raw device twice gave the same hash.
//
// Every write ps2hdd makes to the HDD goes through the same driver, so this is
// not only a matter of what it reads back.
const singleThreaded = "-s"

// MountArgs builds the pfsfuse argument vector. Exported and pure so the
// generated command can be unit tested and shown by --dry-run.
func MountArgs(device, partition, mountpoint string, allowOther bool) ([]string, error) {
	if device == "" {
		return nil, fmt.Errorf("mount: no device")
	}
	if partition == "" {
		return nil, fmt.Errorf("mount: no partition name")
	}
	if mountpoint == "" {
		return nil, fmt.Errorf("mount: no mountpoint")
	}
	args := []string{singleThreaded, "--partition=" + partition}
	if allowOther {
		args = append(args, "-o", "allow_other")
	}
	return append(args, device, mountpoint), nil
}

// Mount mounts a PFS partition at mountpoint, which must already exist.
func (p PFS) Mount(ctx context.Context, device, partition, mountpoint string) error {
	args, err := MountArgs(device, partition, mountpoint, p.AllowOther)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(mountpoint); err != nil || !fi.IsDir() {
		return fmt.Errorf("mountpoint %s is not a directory", mountpoint)
	}
	_, err = p.Runner.Run(ctx, Command{
		Name:       PFSFuseTool,
		Args:       args,
		Privileged: true,
	})
	if err != nil {
		return fmt.Errorf("mount %s from %s: %w", partition, device, err)
	}
	return nil
}

// UnmountArgs builds the fusermount argument vector.
func UnmountArgs(mountpoint string) []string { return []string{"-u", mountpoint} }

// Unmount releases a FUSE mount. fusermount3 is preferred; the older
// fusermount is accepted as a fallback because some distributions still ship
// only that name.
func (p PFS) Unmount(ctx context.Context, mountpoint string) error {
	tool := FusermountTool
	if _, err := p.Runner.Look(tool); err != nil {
		tool = FusermountLegcy
	}
	_, err := p.Runner.Run(ctx, Command{
		Name: tool,
		Args: UnmountArgs(mountpoint),
	})
	if err != nil {
		return fmt.Errorf("unmount %s: %w", mountpoint, err)
	}
	return nil
}

// ListPartitions runs pfsshell's `ls` against a device to enumerate its
// partitions. ps2hdd reads the APA table natively for this, so the command
// exists as a cross-check for `ps2hdd doctor` rather than as the primary path.
func (p PFS) ListPartitions(ctx context.Context, device string) ([]string, error) {
	script := "device " + device + "\nls\nexit\n"
	res, err := p.Runner.Run(ctx, Command{
		Name:       PFSShellTool,
		Privileged: true,
		Stdin:      strings.NewReader(script),
	})
	if err != nil {
		return nil, err
	}
	return ParsePFSShellLs(res.Stdout), nil
}

// MkPartScript builds the pfsshell command script that creates one PFS
// partition. Exported and pure so it can be unit tested and shown by
// --dry-run.
//
// pfsshell is an interactive shell driven here through stdin. `mkpart` takes a
// size ending in M or G and an fs type; PFS is the only type it formats.
// Larger sizes are not one extent: APA allocates a main partition plus
// sub-partitions, and pfsshell decides that split itself. Reproducing that
// allocation here is exactly what this package exists to avoid.
func MkPartScript(device, name, size, fstype string) string {
	return "device " + device + "\nmkpart " + quotePFSArg(name) + " " + size + " " + fstype + "\nexit\n"
}

// CreatePartition creates a PFS partition through pfsshell and returns
// everything the shell printed.
//
// The output is the only thing worth returning, because the exit status is
// worthless: pfsshell is a shell, so a failed `mkpart` prints
// "(!) Exit code is -1." and the shell then exits 0 anyway. The caller must
// confirm the result by re-reading the partition table rather than trusting
// this to have worked.
func (p PFS) CreatePartition(ctx context.Context, device, name, size, fstype string) (string, error) {
	res, err := p.Runner.Run(ctx, Command{
		Name:       PFSShellTool,
		Privileged: true,
		Stdin:      strings.NewReader(MkPartScript(device, name, size, fstype)),
	})
	return res.Stdout + res.Stderr, err
}

// RmPartScript builds the pfsshell command script that removes one partition.
//
// hdl_dump has no verb for this. It had one -- CMD_HIDE, spelled "delete" --
// and upstream compiled it out: config.h carries `#undef INCLUDE_HIDE_CMD`
// with the comment "Hide function is malfunction". A build without it does not
// recognise the word, prints its usage and exits 100, which is what removing a
// game did.
//
// pfsshell has rmpart, and it is the same tool that creates partitions here for
// the same reason: the reference implementation decides how APA space is laid
// out, and this package exists to avoid reimplementing that.
func RmPartScript(device, name string) string {
	return "device " + device + "\nrmpart " + quotePFSArg(name) + "\nexit\n"
}

// quotePFSArg wraps a pfsshell argument in double quotes.
//
// An HDL partition is named after its game -- PP.SLUS_210.50.Burnout 3 Takedow
// -- so it usually contains spaces, and pfsshell splits on whitespace unless a
// token is quoted (util.c, parse_line). Unquoted, `rmpart` received the first
// word and did nothing.
//
// A quote inside the name has no escape in that parser: it returns -1 for a
// quote in the middle of a token. Names come from the APA table rather than
// from a user, so this has nowhere to arise, and leaving it unquoted would be
// worse than the caller seeing a name it cannot pass on.
func quotePFSArg(s string) string { return `"` + s + `"` }

// RemovePartition deletes an APA partition through pfsshell and returns
// everything the shell printed.
//
// As with CreatePartition the exit status is worthless -- pfsshell is a shell,
// and it exits 0 whether or not the command inside it worked -- so the caller
// must confirm by re-reading the partition table.
func (p PFS) RemovePartition(ctx context.Context, device, name string) (string, error) {
	res, err := p.Runner.Run(ctx, Command{
		Name:       PFSShellTool,
		Privileged: true,
		Stdin:      strings.NewReader(RmPartScript(device, name)),
	})
	return res.Stdout + res.Stderr, err
}

// ParsePFSShellLs extracts partition names from pfsshell's `ls` output. The
// shell echoes prompts and banners around the listing, so only lines that look
// like APA partition ids are kept.
func ParsePFSShellLs(out string) []string {
	var parts []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimRight(line, "\r"))
		// Strip any prompt the shell wrote before the listing.
		if i := strings.LastIndex(line, "# "); i >= 0 {
			line = strings.TrimSpace(line[i+2:])
		}
		if line == "" {
			continue
		}
		for _, f := range strings.Fields(line) {
			if isPartitionID(f) {
				parts = append(parts, f)
			}
		}
	}
	return parts
}

// isPartitionID reports whether a token looks like an APA partition id.
// FreeHDBoot partitions are either "__"-prefixed system partitions, "+"
// prefixed application partitions, or "PP."-prefixed game partitions.
func isPartitionID(s string) bool {
	switch {
	case strings.HasPrefix(s, "__"), strings.HasPrefix(s, "+"), strings.HasPrefix(s, "PP."):
		return len(s) > 1 && len(s) <= 32
	}
	return false
}
