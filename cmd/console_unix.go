//go:build !windows

package cmd

// prepareConsole 在 POSIX 上无事可做:终端本来就是 UTF-8 + ANSI。
func prepareConsole() {}

// consoleSupportsANSI:POSIX 终端默认支持 ANSI。
func consoleSupportsANSI() bool { return true }
