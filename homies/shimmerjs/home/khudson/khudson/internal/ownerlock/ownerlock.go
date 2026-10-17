// Package ownerlock serializes service lifetimes across processes.
package ownerlock

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

var ErrHeld = errors.New("service owner already running")

// Acquire returns a nonblocking exclusive lock. Close releases ownership.
// The lock file must never be removed: all contenders must lock the same inode.
func Acquire(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, fmt.Errorf("%s: %w", path, ErrHeld)
		}
		return nil, fmt.Errorf("lock %s: %w", path, err)
	}
	return f, nil
}
