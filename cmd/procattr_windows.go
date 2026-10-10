//go:build windows

package cmd

import (
	"syscall"

	"golang.org/x/sys/windows"
)

// detachProcAttr 是 fork 后台刷新进程用的进程属性。
//
// Windows 上子进程默认继承父进程的控制台:父进程(statusline,由 Claude Code
// 拉起)退出或它的控制台关闭时,子进程会被连带杀掉,自愈刷新就白跑了。
// DETACHED_PROCESS 让它不挂任何控制台;CREATE_NEW_PROCESS_GROUP 隔开
// Ctrl-C 广播(用户在终端里 Ctrl-C 不会打断正在写缓存的刷新);
// HideWindow 免掉控制台窗口闪现。
func detachProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.DETACHED_PROCESS | windows.CREATE_NEW_PROCESS_GROUP,
	}
}
