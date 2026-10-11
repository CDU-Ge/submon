//go:build !windows

package statuslinebundle

import (
	"io/fs"
)

func Binary() ([]byte, error) { return nil, fs.ErrNotExist }
