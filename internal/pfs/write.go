// Package pfs holds the small number of rules that apply to writing into a
// PFS partition mounted over FUSE.
//
// There is one rule, and it has been rediscovered three times: never truncate.
package pfs

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// Create opens path for writing, replacing whatever was there.
//
// os.Create is O_RDWR|O_CREAT|O_TRUNC, and O_TRUNC on a file that already
// exists makes the kernel ask the filesystem to truncate it. pfsfuse
// implements ftruncate but not truncate -- there is a .ftruncate in its
// fuse_operations table and no .truncate -- so the request comes back ENOSYS,
// surfacing as "function not implemented".
//
// The shape of that failure is why it keeps being rediscovered: writing a file
// for the first time calls .create and works, so a fresh partition behaves
// perfectly and a populated one fails on every single file. Unlinking asks for
// nothing pfsfuse does not implement.
//
// There is deliberately no write-aside and rename here. PFS over FUSE does not
// reliably support rename, so the old file goes before the new one arrives: an
// interrupted write leaves the destination absent rather than stale. Absent is
// the better of the two, because it is visibly incomplete and a rerun fixes
// it, while a stale file looks correct forever.
func Create(path string, perm os.FileMode) (*os.File, error) {
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
}
