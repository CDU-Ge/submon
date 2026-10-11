//go:build windows

package cmd

import (
	"golang.org/x/sys/windows"
	"os"
)

func lockStatuslineFile(f *os.File) error {
	var ol windows.Overlapped
	return windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &ol)
}
