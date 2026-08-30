//go:build !unix

package app

import "errors"

// freeSpace is unavailable off unix. Callers treat an error as "unknown" and
// proceed, so a platform without statfs loses the pre-flight check rather than
// the feature.
func freeSpace(string) (int64, error) {
	return 0, errors.New("free space is not reported on this platform")
}
