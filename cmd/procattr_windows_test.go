//go:build windows

package cmd

import (
	"context"
	"testing"

	"golang.org/x/sys/windows"
)

// TestGitSpawnHidesConsole 守住 Windows 上"渲染路径的子进程不许弹控制台
// 窗口"的契约。statusline 由 Claude Code 以无控制台方式拉起(管道收
// stdout),任何没挂 hideWindowProcAttr 的控制台子进程(git 最典型)都会
// 新建控制台窗口 —— 每渲染一帧闪一次黑窗。
func TestGitSpawnHidesConsole(t *testing.T) {
	attr := gitCmd(context.Background(), "status", "--porcelain=v2", "--branch").SysProcAttr
	if attr == nil {
		t.Fatal("git 子进程没有进程属性:Windows 上会弹控制台窗口")
	}
	if !attr.HideWindow {
		t.Error("git 子进程缺 HideWindow")
	}
	if attr.CreationFlags&windows.CREATE_NO_WINDOW == 0 {
		t.Errorf("git 子进程缺 CREATE_NO_WINDOW(CreationFlags=%#x)", attr.CreationFlags)
	}
}
