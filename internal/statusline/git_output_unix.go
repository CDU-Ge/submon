//go:build !windows

package statusline

import "os/exec"

func gitOutput(c *exec.Cmd) ([]byte, error) { return c.Output() }
