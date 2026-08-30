//go:build unix

package app

import "golang.org/x/sys/unix"

// freeSpace reports the bytes available to an unprivileged user on the
// filesystem holding path.
func freeSpace(path string) (int64, error) {
	var st unix.Statfs_t
	if err := unix.Statfs(path, &st); err != nil {
		return 0, err
	}
	return int64(st.Bavail) * int64(st.Bsize), nil
}
