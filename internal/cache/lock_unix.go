//go:build !windows

package cache

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/unix"
)

// lockFile opens/creates <path>.lock and takes an exclusive flock. The lock is
// released by closing the returned file (also on process death, like flock).
func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := unix.Flock(int(f.Fd()), unix.LOCK_EX); err != nil {
		f.Close()
		return nil, fmt.Errorf("flock %s: %w", path+".lock", err)
	}
	return f, nil
}
