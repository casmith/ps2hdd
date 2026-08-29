package drive

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/casmith/ps2hdd/internal/config"
	"github.com/casmith/ps2hdd/internal/external"
	"github.com/casmith/ps2hdd/internal/logging"
)

// MountManager owns every PFS mount ps2hdd creates.
//
// Two rules drive the design. First, a mount this process did not create is
// never unmounted: a user may have mounted +OPL by hand in another terminal,
// and pulling it out from under them would be rude at best. Second, every
// mount this process *did* create is released on exit, including on SIGINT and
// SIGTERM, because a leaked FUSE mount keeps the HDD busy and confuses the
// next run.
//
// Mount and unmount are serialised: pfsfuse works on the raw device, and two
// concurrent mounts of different partitions on the same disk are not something
// the underlying tools promise to handle.
type MountManager struct {
	PFS    external.PFS
	Device string

	mu    sync.Mutex
	owned map[string]*mount // partition -> mount
	root  string
}

type mount struct {
	partition  string
	mountpoint string
	refs       int
}

// NewMountManager creates a manager for a device.
func NewMountManager(pfs external.PFS, device string) *MountManager {
	return &MountManager{PFS: pfs, Device: device, owned: map[string]*mount{}}
}

// ErrAlreadyMounted means the partition is mounted by something other than
// this process.
var ErrAlreadyMounted = errors.New("partition is already mounted by another process")

// Root returns the runtime directory holding this process's mountpoints,
// creating it if needed.
func (m *MountManager) Root() (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.rootLocked()
}

func (m *MountManager) rootLocked() (string, error) {
	if m.root != "" {
		return m.root, nil
	}
	base, err := config.RuntimeDir()
	if err != nil {
		return "", err
	}
	// A per-process directory keeps two concurrent ps2hdd runs from adopting
	// each other's mountpoints.
	dir := filepath.Join(base, fmt.Sprintf("mnt-%d", os.Getpid()))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create mount root %s: %w", dir, err)
	}
	m.root = dir
	return dir, nil
}

// Mount mounts a partition and returns its mountpoint.
//
// Mounting a partition that is already mounted by this manager bumps a
// reference count instead of mounting again, so nested callers (an asset sync
// inside an install, say) each get a valid path and the partition is released
// only when the last of them is done.
func (m *MountManager) Mount(ctx context.Context, partition string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if mt, ok := m.owned[partition]; ok {
		mt.refs++
		return mt.mountpoint, nil
	}

	root, err := m.rootLocked()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(root, mountDirName(partition))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", dir, err)
	}

	// A stale mountpoint from a previous run that crashed shows up as a
	// directory that is still a mount. Adopting it would mean unmounting
	// something we did not create, so it is reported instead.
	if mounted, err := IsMountpoint(dir); err == nil && mounted {
		return "", fmt.Errorf("%w: %s is already a mount left over from a previous run; unmount it with `fusermount3 -u %s`",
			ErrAlreadyMounted, dir, dir)
	}

	if err := m.PFS.Mount(ctx, m.Device, partition, dir); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	m.owned[partition] = &mount{partition: partition, mountpoint: dir, refs: 1}
	logging.ContextLogger(ctx).Info("mounted PFS partition",
		"partition", partition, "mountpoint", dir, "device", m.Device)
	return dir, nil
}

// Unmount releases one reference to a partition and unmounts it when the last
// reference goes away.
func (m *MountManager) Unmount(ctx context.Context, partition string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.unmountLocked(ctx, partition)
}

func (m *MountManager) unmountLocked(ctx context.Context, partition string) error {
	mt, ok := m.owned[partition]
	if !ok {
		// Not ours: silently do nothing rather than unmount a user's mount.
		return nil
	}
	mt.refs--
	if mt.refs > 0 {
		return nil
	}
	delete(m.owned, partition)
	if err := m.PFS.Unmount(ctx, mt.mountpoint); err != nil {
		return err
	}
	_ = os.Remove(mt.mountpoint)
	logging.ContextLogger(ctx).Info("unmounted PFS partition",
		"partition", partition, "mountpoint", mt.mountpoint)
	return nil
}

// With mounts a partition, runs fn, and unmounts it afterwards even if fn
// panics or returns an error. This is the only form callers should use.
func (m *MountManager) With(ctx context.Context, partition string, fn func(mountpoint string) error) (err error) {
	mp, err := m.Mount(ctx, partition)
	if err != nil {
		return err
	}
	defer func() {
		// A cancelled context would make the unmount fail too, so cleanup runs
		// on a context that is still live.
		cleanupCtx := context.WithoutCancel(ctx)
		if uerr := m.Unmount(cleanupCtx, partition); uerr != nil && err == nil {
			err = uerr
		}
	}()
	return fn(mp)
}

// Close releases every mount this manager created. It is safe to call twice
// and is what the signal handler and the TUI's shutdown path both call.
func (m *MountManager) Close(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var firstErr error
	for partition, mt := range m.owned {
		delete(m.owned, partition)
		if err := m.PFS.Unmount(ctx, mt.mountpoint); err != nil && firstErr == nil {
			firstErr = err
			continue
		}
		_ = os.Remove(mt.mountpoint)
	}
	if m.root != "" {
		_ = os.Remove(m.root)
	}
	return firstErr
}

// Persistent mounts.
//
// Everything above is an ephemeral mount: created for one operation, tracked,
// and released when the process exits. That is right for an install or an
// artwork sync, and wrong for `ps2hdd mount +OPL`, where the user is asking
// for a mount they can then use from a shell.
//
// A persistent mount therefore lives at a stable, process-independent path and
// is *not* tracked for cleanup. `ps2hdd unmount` releases it later, possibly
// from a different process. The safety property still holds: only paths inside
// ps2hdd's own runtime directory are ever unmounted, so a mount the user made
// somewhere else is untouchable.

// persistentRoot is the directory holding stable mountpoints.
func (m *MountManager) persistentRoot() (string, error) {
	base, err := config.RuntimeDir()
	if err != nil {
		return "", err
	}
	dir := filepath.Join(base, "mnt")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create mount root %s: %w", dir, err)
	}
	return dir, nil
}

// PersistentPath reports where a partition would be mounted persistently.
func (m *MountManager) PersistentPath(partition string) (string, error) {
	root, err := m.persistentRoot()
	if err != nil {
		return "", err
	}
	return filepath.Join(root, mountDirName(partition)), nil
}

// MountPersistent mounts a partition at its stable path and leaves it there.
func (m *MountManager) MountPersistent(ctx context.Context, partition string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	dir, err := m.PersistentPath(partition)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("create mountpoint %s: %w", dir, err)
	}
	if mounted, err := IsMountpoint(dir); err == nil && mounted {
		// Already mounted, by us or by an earlier run. Returning the path is
		// the useful answer; unmounting and remounting would be worse.
		return dir, nil
	}
	if err := m.PFS.Mount(ctx, m.Device, partition, dir); err != nil {
		_ = os.Remove(dir)
		return "", err
	}
	logging.ContextLogger(ctx).Info("mounted PFS partition persistently",
		"partition", partition, "mountpoint", dir, "device", m.Device)
	return dir, nil
}

// UnmountPersistent releases a stable mount, whichever process created it.
func (m *MountManager) UnmountPersistent(ctx context.Context, partition string) error {
	dir, err := m.PersistentPath(partition)
	if err != nil {
		return err
	}
	return m.UnmountPath(ctx, dir)
}

// UnmountPath releases a stable mount by its path.
//
// The path must be inside ps2hdd's own runtime mount directory. That check is
// the whole safety story for this function: without it, `ps2hdd unmount` would
// be a way to unmount anything on the machine.
func (m *MountManager) UnmountPath(ctx context.Context, path string) error {
	root, err := m.persistentRoot()
	if err != nil {
		return err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	if !strings.HasPrefix(abs, root+string(filepath.Separator)) {
		return fmt.Errorf("refusing to unmount %s: it is not a mount ps2hdd created", path)
	}
	if !isMountedOrStandIn(abs) {
		return fmt.Errorf("%s is not mounted", abs)
	}
	if err := m.PFS.Unmount(ctx, abs); err != nil {
		return err
	}
	_ = os.Remove(abs)
	logging.ContextLogger(ctx).Info("released persistent PFS mount", "mountpoint", abs)
	return nil
}

// ListPersistent returns the stable mountpoints that are currently mounted,
// keyed by the directory name derived from the partition id.
func (m *MountManager) ListPersistent() (map[string]string, error) {
	root, err := m.persistentRoot()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	out := map[string]string{}
	for _, e := range entries {
		path := filepath.Join(root, e.Name())
		if isMountedOrStandIn(path) {
			out[e.Name()] = path
		}
	}
	return out, nil
}

// isMountedOrStandIn reports whether a path under ps2hdd's runtime mount root
// is something to release.
//
// Normally that means the kernel lists it as a mountpoint. Demo mode stands a
// symlink in for a FUSE mount -- a real mount needs privileges the demo does
// not have, and every caller in this program only reads and writes ordinary
// files under the mountpoint -- and mountinfo naturally does not list one, so
// a symlink counts too. The safety property is the containment check in
// UnmountPath, not this.
func isMountedOrStandIn(path string) bool {
	if mounted, err := IsMountpoint(path); err == nil && mounted {
		return true
	}
	fi, err := os.Lstat(path)
	return err == nil && fi.Mode()&os.ModeSymlink != 0
}

// Owned lists the partitions this manager currently has mounted.
func (m *MountManager) Owned() map[string]string {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]string, len(m.owned))
	for k, v := range m.owned {
		out[k] = v.mountpoint
	}
	return out
}

// mountDirName turns a partition id into a directory name. APA ids contain
// characters that are awkward in paths ('+' and leading dots), so they are
// mapped to a readable equivalent.
func mountDirName(partition string) string {
	name := strings.TrimPrefix(partition, "+")
	name = strings.TrimPrefix(name, "__.")
	name = strings.TrimPrefix(name, "__")
	name = strings.Map(func(r rune) rune {
		if r == '/' || r == 0 {
			return '_'
		}
		return r
	}, name)
	name = strings.ToLower(strings.Trim(name, ". "))
	if name == "" {
		name = "partition"
	}
	return name
}

// IsMountpoint reports whether a directory is currently a mount point.
func IsMountpoint(dir string) (bool, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	f, err := os.Open(mountinfoPath)
	if err != nil {
		return false, err
	}
	defer f.Close()
	mounts, err := parseMountinfoPoints(f)
	if err != nil {
		return false, err
	}
	return mounts[abs], nil
}

// parseMountinfoPoints returns the set of active mountpoints. FUSE mounts have
// a source that is not under /dev, so the device-oriented parser above does
// not see them.
func parseMountinfoPoints(r interface{ Read([]byte) (int, error) }) (map[string]bool, error) {
	out := map[string]bool{}
	buf := make([]byte, 0, 64*1024)
	tmp := make([]byte, 32*1024)
	for {
		n, err := r.Read(tmp)
		buf = append(buf, tmp[:n]...)
		if err != nil {
			break
		}
	}
	for _, line := range strings.Split(string(buf), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		out[unescapeMountinfo(fields[4])] = true
	}
	return out, nil
}
