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

// hideWindowProcAttr 是 fork 短命控制台子进程(如 git)用的进程属性。
//
// Claude Code 以无控制台方式拉起 statusline(管道收 stdout),此时任何
// 没做处理的控制台子程序都会**新建一个控制台窗口** —— 每渲染一帧闪一次
// 黑窗。CREATE_NO_WINDOW 让子进程干脆不分配控制台;HideWindow 补上
// STARTF_USESHOWWINDOW 双保险(与 detachProcAttr 的区别:这里要同步等
// 结果,不能脱离父进程)。
func hideWindowProcAttr() *syscall.SysProcAttr {
	return &syscall.SysProcAttr{
		HideWindow:    true,
		CreationFlags: windows.CREATE_NO_WINDOW,
	}
}
