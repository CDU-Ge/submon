//go:build !windows

package cmd

import "os/exec"

func gitOutput(c *exec.Cmd) ([]byte, error) { return c.Output() }
