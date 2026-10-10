//go:build windows

package cache

import (
	"fmt"
	"os"
	"path/filepath"

	"golang.org/x/sys/windows"
)

// lockFile opens/creates <path>.lock and takes an exclusive lock over its first
// byte via LockFileEx — the Windows equivalent of flock(LOCK_EX). LockFileEx
// blocks while another process holds the range, so callers get the same
// serialize-writers semantics as on POSIX. The lock is released by closing the
// returned file (and by the OS if the process dies).
func lockFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path+".lock", os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	var ol windows.Overlapped
	if err := windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK, 0, 1, 0, &ol); err != nil {
		f.Close()
		return nil, fmt.Errorf("LockFileEx %s: %w", path+".lock", err)
	}
	return f, nil
}
