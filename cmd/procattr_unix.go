//go:build !windows

package cmd

import "syscall"

// detachProcAttr 是 fork 后台刷新进程用的进程属性。
//
// POSIX 上返回 nil:子进程继承父进程的会话,父进程(statusline)退出后
// 它被 init 收养,本来就不受父进程生死影响 —— 不需要额外动作。
func detachProcAttr() *syscall.SysProcAttr { return nil }

// hideWindowProcAttr 是 fork 短命控制台子进程(如 git)用的进程属性。
// POSIX 没有"控制台窗口"概念,不需要额外动作。
func hideWindowProcAttr() *syscall.SysProcAttr { return nil }
