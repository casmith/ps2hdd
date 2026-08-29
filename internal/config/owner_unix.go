package config

import (
	"fmt"
	"os"
	"syscall"
)

// checkPrivateDir verifies dir is a directory owned by the current user with
// no group or world permissions. The fallback runtime root lives in a
// world-writable /tmp, so a pre-existing path there may have been planted by
// somebody else; mounting a HDD under it would hand them the contents.
func checkPrivateDir(dir string) error {
	fi, err := os.Lstat(dir)
	if err != nil {
		return fmt.Errorf("stat runtime directory %s: %w", dir, err)
	}
	if !fi.IsDir() {
		return fmt.Errorf("runtime path %s exists and is not a directory", dir)
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !ok {
		return nil
	}
	if int(st.Uid) != os.Getuid() {
		return fmt.Errorf("runtime directory %s is owned by uid %d, not %d; refusing to use it", dir, st.Uid, os.Getuid())
	}
	if fi.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("runtime directory %s is accessible to other users (mode %o); refusing to use it", dir, fi.Mode().Perm())
	}
	return nil
}
