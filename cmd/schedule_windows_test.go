//go:build windows

package cmd

import (
	"strings"
	"testing"
	"unicode/utf16"
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
