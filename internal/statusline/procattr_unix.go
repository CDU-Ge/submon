//go:build !windows

package statusline

import "syscall"

// hideWindowProcAttr 是 fork 短命控制台子进程(如 git)用的进程属性。
// POSIX 没有"控制台窗口"概念,不需要额外动作。
func hideWindowProcAttr() *syscall.SysProcAttr { return nil }
