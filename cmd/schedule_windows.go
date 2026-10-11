//go:build windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"unicode/utf16"

	"github.com/ge/subscription-monitor/internal/appdir"
	"github.com/spf13/cobra"
)

// Windows 任务计划程序实现:注册一条每 N 分钟跑一次 `submon watch` 的任务。
//
// 任务不直接执行 submon.exe,而是执行 wscript.exe 跑一个生成的 VBS 启动器 —
// 任务计划程序在交互式会话里起控制台程序会每 N 分钟闪一个黑窗,VBS 的
// Run(..., 0, ...) 以隐藏窗口启动,用户完全无感。

const taskName = "submon-watch"

type windowsScheduler struct{}

func newScheduler() scheduler { return windowsScheduler{} }

func (windowsScheduler) Name() string { return "Windows 任务计划程序" }

// schtasksRun 是调用 schtasks 的唯一出口。抽成变量是为了测试能注入替身 ——
// 真实注册会在用户机器上留下计划任务,测试绝不能碰。
var schtasksRun = func(args ...string) ([]byte, error) {
	return exec.Command("schtasks", args...).CombinedOutput()
}

func (windowsScheduler) Install(cmd *cobra.Command, bin, interval string) error {
	minutes, err := intervalMinutes(interval)
	if err != nil {
		return err
	}
	launcher, err := launcherPath()
	if err != nil {
		return err
	}
	if err := os.WriteFile(launcher, launcherBytes(bin), 0o644); err != nil {
		return fmt.Errorf("写入启动器 %s 失败: %w\n"+
			"  该路径写不进去通常是两种原因:在受限 shell(沙箱)里跑,或目录只读。\n"+
			"  请在普通 PowerShell 窗口重跑;想先看内容用 `submon install --print`。", launcher, err)
	}
	out, err := schtasksRun(createArgs(minutes, launcher)...)
	if err != nil {
		return fmt.Errorf("schtasks /Create: %s", strings.TrimSpace(string(out)))
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已注册计划任务:", taskName)
	fmt.Fprintln(cmd.OutOrStdout(), "启动器:", launcher)
	fmt.Fprintln(cmd.OutOrStdout(), "调度间隔: 每", minutes, "分钟")
	return nil
}

func (windowsScheduler) Uninstall(cmd *cobra.Command) error {
	if err := exec.Command("schtasks", "/Query", "/TN", taskName).Run(); err == nil {
		// /F 免掉交互确认;查询已确认任务存在,失败就是真失败。
		if out, err := schtasksRun("/Delete", "/F", "/TN", taskName); err != nil {
			return fmt.Errorf("schtasks /Delete: %s", strings.TrimSpace(string(out)))
		}
	}
	if launcher, err := launcherFilePath(); err == nil {
		_ = os.Remove(launcher)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已删除计划任务:", taskName)
	return nil
}

func (windowsScheduler) Print(cmd *cobra.Command, bin, interval string) error {
	minutes, err := intervalMinutes(interval)
	if err != nil {
		return err
	}
	launcher, err := launcherFilePath()
	if err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "--- %s.vbs ---\n%s", taskName, launcherText(bin))
	fmt.Fprintf(cmd.OutOrStdout(), "--- schtasks ---\n%s\n", strings.Join(createArgs(minutes, launcher), " "))
	return nil
}

// Status 先问 schtasks 任务在不在(退出码跨语言稳定),再用 PowerShell 取
// 状态与下次运行时间 —— schtasks 的文本输出是本地化的,解析它会随系统
// 语言崩,而 PowerShell 的属性名不本地化。
func (windowsScheduler) Status() scheduleStatus {
	if _, err := exec.LookPath("schtasks"); err != nil {
		return scheduleStatus{Missing: true}
	}
	if err := exec.Command("schtasks", "/Query", "/TN", taskName).Run(); err != nil {
		return scheduleStatus{Note: "未注册"}
	}
	st := scheduleStatus{Ready: true, Note: "已注册"}
	const query = `$t = Get-ScheduledTask -TaskName '` + taskName + `' -ErrorAction Stop; ` +
		`$i = $t | Get-ScheduledTaskInfo; ` +
		`Write-Output "state=$($t.State)"; ` +
		`if ($i.NextRunTime) { Write-Output "next=$($i.NextRunTime.ToString('yyyy-MM-dd HH:mm'))" } else { Write-Output 'next=' }`
	out, err := runPowerShell(query)
	if err != nil {
		return st // 没装 PowerShell:任务确实在,下次时间未知而已。
	}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "state="):
			st.Note = strings.TrimPrefix(line, "state=")
			st.Ready = !strings.EqualFold(st.Note, "Disabled")
		case strings.HasPrefix(line, "next="):
			st.NextRun = strings.TrimPrefix(line, "next=")
		}
	}
	return st
}

// createArgs 拼 schtasks 注册命令。不加 /RU /RP:任务默认只在用户登录时
// 以当前用户身份运行 —— 配额提醒本来就是给人看的,不需要存密码。
func createArgs(minutes int, launcher string) []string {
	return []string{
		"/Create", "/F",
		"/TN", taskName,
		"/TR", `wscript.exe "` + launcher + `"`,
		"/SC", "MINUTE", "/MO", strconv.Itoa(minutes),
	}
}

// launcherFilePath 是 VBS 启动器位置(配置目录,与 config.toml 同级)。
// 只算路径、不建目录 —— --print 与卸载都不该有副作用。
func launcherFilePath() (string, error) {
	dir, err := appdir.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, taskName+".vbs"), nil
}

// launcherPath 在 launcherFilePath 之上确保目录存在(写文件之前用)。
func launcherPath() (string, error) {
	p, err := launcherFilePath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	return p, nil
}

// launcherText 是 VBS 源码:隐藏窗口启动一次 `submon watch`,不等待其结束。
// VBS 字符串内的 "" 表示一个引号,用来包住可能含空格的 exe 路径。
func launcherText(bin string) string {
	return "' submon: 由 `submon install` 生成,请勿手工编辑。\r\n" +
		"' 静默运行一次 submon watch(任务计划程序直接起 exe 会闪控制台窗口)。\r\n" +
		"Set sh = CreateObject(\"WScript.Shell\")\r\n" +
		"sh.Run \"\"\"" + bin + "\"\" watch\", 0, False\r\n"
}

// launcherBytes 把 VBS 源码编码成 UTF-16LE + BOM 再落盘。
//
// wscript.exe 只把"带 BOM 的 UTF-16LE"当 Unicode 脚本读,UTF-8 文件会按系统
// 代码页(中文 Windows 是 GBK)解码 —— 用户名或安装路径里有中文时,
// exe 路径直接变成乱码,任务每次触发都失败。纯 ASCII 路径看不出区别,
// 所以必须无条件按 Unicode 写。
func launcherBytes(bin string) []byte {
	units := utf16.Encode([]rune(launcherText(bin)))
	out := make([]byte, 0, 2+len(units)*2)
	out = append(out, 0xFF, 0xFE) // UTF-16LE BOM
	for _, u := range units {
		out = append(out, byte(u), byte(u>>8))
	}
	return out
}
