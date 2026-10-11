//go:build windows

package cmd

import (
	"strings"
	"testing"
)

// TestStatuslineCommandNoBackslash 守住 Windows 上最坑的一条:Claude Code
// 经 Git Bash 执行 statusLine 命令,未加引号的反斜杠会被当转义符吃掉 ——
//
//	E:\Tools\submon-windows_amd64.exe statusline
//	→ E:Toolssubmon-windows_amd64.exe statusline   (静默失败,状态栏空白)
//
// 官方文档 Troubleshooting → "Status line not appearing" 明写这条。
func TestStatuslineCommandNoBackslash(t *testing.T) {
	got := statuslineCommand(`E:\Tools\submon-windows_amd64.exe`)
	if strings.Contains(got, `\`) {
		t.Fatalf("Windows 路径必须转正斜杠,得到 %q", got)
	}
	if got != "E:/Tools/submon-windows_amd64.exe statusline" {
		t.Errorf("命令 = %q, want %q", got, "E:/Tools/submon-windows_amd64.exe statusline")
	}

	spaced := statuslineCommand(`C:\Program Files\submon.exe`)
	if spaced != `"C:/Program Files/submon.exe" statusline` {
		t.Errorf("含空格路径 = %q, want %q", spaced, `"C:/Program Files/submon.exe" statusline`)
	}
}
