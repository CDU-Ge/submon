//go:build windows

package cmd

import (
	"bytes"
	"os"
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/spf13/cobra"
)

// TestLauncherBytesIsUTF16LEWithBOM 守住 VBS 的编码:wscript 只把带 BOM 的
// UTF-16LE 当 Unicode 脚本读,退回 UTF-8 时含中文的安装路径会变乱码。
func TestLauncherBytesIsUTF16LEWithBOM(t *testing.T) {
	raw := launcherBytes(`C:\Program Files\submon\submon.exe`)
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Fatalf("VBS 缺 UTF-16LE BOM: % x", raw[:min(4, len(raw))])
	}
	if (len(raw)-2)%2 != 0 {
		t.Fatalf("UTF-16LE 负载字节数应为偶数,得到 %d", len(raw)-2)
	}
	units := make([]uint16, 0, (len(raw)-2)/2)
	for i := 2; i+1 < len(raw); i += 2 {
		units = append(units, uint16(raw[i])|uint16(raw[i+1])<<8)
	}
	got := string(utf16.Decode(units))

	// VBS 里 "" 表示一个引号:含空格的 exe 路径必须被引号包住。
	want := `sh.Run """C:\Program Files\submon\submon.exe"" watch", 0, False`
	if !strings.Contains(got, want) {
		t.Errorf("启动命令不对:\n得到 %q\n期望含 %q", got, want)
	}
}

// TestWindowsSchedulerInstallEndToEnd 把 Windows 的安装路径整条真跑一遍:
// 写 VBS 启动器 + 拼 schtasks 参数。schtasks 走注入的替身、appdir 走隔离
// 临时目录,所以既不会在用户机器上注册真任务,也不碰真实 %AppData%。
func TestWindowsSchedulerInstallEndToEnd(t *testing.T) {
	isolateAppDirs(t, t.TempDir())

	var gotArgs []string
	orig := schtasksRun
	schtasksRun = func(args ...string) ([]byte, error) {
		gotArgs = args
		return []byte("SUCCESS: 已创建计划任务"), nil
	}
	t.Cleanup(func() { schtasksRun = orig })

	var out bytes.Buffer
	c := &cobra.Command{}
	c.SetOut(&out)
	if err := newScheduler().Install(c, `C:\Tools\submon.exe`, "15m"); err != nil {
		t.Fatalf("Install 失败: %v", err)
	}

	launcher, err := launcherFilePath()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(launcher)
	if err != nil {
		t.Fatalf("启动器没写出来: %v", err)
	}
	if len(raw) < 2 || raw[0] != 0xFF || raw[1] != 0xFE {
		t.Errorf("启动器不是 UTF-16LE+BOM: % x", raw[:min(4, len(raw))])
	}

	joined := strings.Join(gotArgs, " ")
	if !strings.Contains(joined, "/MO 15") {
		t.Errorf("schtasks 参数缺 /MO 15: %q", joined)
	}
	if !strings.Contains(joined, launcher) {
		t.Errorf("schtasks 参数没引用刚写的启动器 %q: %q", launcher, joined)
	}
	if !strings.Contains(out.String(), "已注册计划任务") {
		t.Errorf("缺少成功提示: %q", out.String())
	}
}

// TestWindowsSchedulerInstallExplainsUnwritableLauncher 守住报错质量:写不了
// 启动器时(受限 shell / 只读目录)必须说清路径与原因,而不是抛一句裸的
// "Access is denied"。用"同名目录占位"来稳定复现写失败,不需要改权限。
func TestWindowsSchedulerInstallExplainsUnwritableLauncher(t *testing.T) {
	isolateAppDirs(t, t.TempDir())

	launcher, err := launcherFilePath()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(launcher, 0o755); err != nil { // 目录占位:写文件必失败
		t.Fatal(err)
	}

	c := &cobra.Command{}
	err = newScheduler().Install(c, `C:\Tools\submon.exe`, "15m")
	if err == nil {
		t.Fatal("启动器写不进去时应当报错")
	}
	msg := err.Error()
	if !strings.Contains(msg, launcher) {
		t.Errorf("报错应带上路径 %q,得到 %q", launcher, msg)
	}
	if !strings.Contains(msg, "沙箱") {
		t.Errorf("报错应点明受限 shell/沙箱这个常见原因,得到 %q", msg)
	}
}

// TestCreateArgsQuotesLauncher 守住 schtasks 的 /TR:启动器路径可能含空格
// (用户名、Program Files),不引号会被 Task Scheduler 截断成两段。
func TestCreateArgsQuotesLauncher(t *testing.T) {
	const launcher = `C:\Users\a b\AppData\Roaming\submon\submon-watch.vbs`
	args := createArgs(5, launcher)
	var tr string
	for i, a := range args {
		if a == "/TR" && i+1 < len(args) {
			tr = args[i+1]
		}
	}
	if want := `wscript.exe "` + launcher + `"`; tr != want {
		t.Errorf("/TR = %q, 期望 %q", tr, want)
	}
	if !strings.Contains(strings.Join(args, " "), "/MO 5") {
		t.Errorf("缺少 /MO 5: %v", args)
	}
}
