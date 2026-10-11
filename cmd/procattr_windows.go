//go:build windows

package cmd

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// hideWindowProcAttr 是 fork 短命控制台子进程(如 git)用的进程属性。
//
// Claude Code 以无控制台方式拉起 statusline(管道收 stdout),此时任何
// 没做处理的控制台子程序都会**新建一个控制台窗口** —— 每渲染一帧闪一次
// 黑窗。CREATE_NO_WINDOW 让子进程干脆不分配控制台;HideWindow 补上
// STARTF_USESHOWWINDOW 双保险(同步等待结果)。
func hideWindowProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
