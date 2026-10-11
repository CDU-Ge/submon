//go:build !windows

package cmd

import (
	"golang.org/x/sys/unix"
	"os"
)

func lockStatuslineFile(f *os.File) error {
	return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
}
