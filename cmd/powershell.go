package cmd

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// PowerShell helpers shared by the completion installer (all platforms) and
// the Windows scheduler status query. Everything here degrades gracefully:
// PowerShell is never required for submon to work, only for a better answer.

// powershellExe 返回可用的 PowerShell 可执行文件名:优先 pwsh(PowerShell 7+,
// Windows / macOS / Linux 都有),退回 Windows 自带的 powershell。都没装时
// 返回 "powershell",调用方拿到 exec 的 not-found 错误后走各自兜底。
func powershellExe() string {
	for _, exe := range []string{"pwsh", "powershell"} {
		if _, err := exec.LookPath(exe); err == nil {
			return exe
		}
	}
	return "powershell"
}

// runPowerShell 跑一段 PowerShell 脚本并返回去掉首尾空白的 stdout。
//
// 脚本前缀统一把 [Console]::OutputEncoding 设成 UTF-8:输出被重定向(而非
// 打屏)时 PowerShell 默认用控制台代码页编码,中文用户名/路径会被读成乱码。
// 10s 超时 —— 该路径只在交互式诊断里用,不能卡死命令。
func runPowerShell(script string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	const utf8Out = "[Console]::OutputEncoding=[System.Text.Encoding]::UTF8; "
	out, err := exec.CommandContext(ctx, powershellExe(),
		"-NoProfile", "-NonInteractive", "-Command", utf8Out+script).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// powershellProfilePath 问 PowerShell 要 $PROFILE — Windows PowerShell 5.1、
// PowerShell 7 与不同宿主的 profile 路径各不相同,自己拼容易写错。
// 问不到时按 Documents 惯例兜底:已有的目录优先,再默认 PowerShell 7。
func powershellProfilePath() (string, error) {
	if p, err := runPowerShell("$PROFILE"); err == nil && p != "" {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	docs := filepath.Join(home, "Documents")
	for _, dir := range []string{"PowerShell", "WindowsPowerShell"} {
		if fi, err := os.Stat(filepath.Join(docs, dir)); err == nil && fi.IsDir() {
			return filepath.Join(docs, dir, "Microsoft.PowerShell_profile.ps1"), nil
		}
	}
	return filepath.Join(docs, "PowerShell", "Microsoft.PowerShell_profile.ps1"), nil
}
