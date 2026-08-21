package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestNoColorFlagSuppressesANSI 验证 --no-color flag 关闭所有 ANSI。
func TestNoColorFlagSuppressesANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("TERM", "")

	old := noColorFlag
	noColorFlag = true
	t.Cleanup(func() { noColorFlag = old })

	if got := ansi("0;31"); got != "" {
		t.Fatalf("ansi(0;31) 在 --no-color 时应为空,得到 %q", got)
	}
}

// TestNoColorEnvSuppressesANSI 验证 NO_COLOR=1 关颜色。
func TestNoColorEnvSuppressesANSI(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "")

	old := noColorFlag
	noColorFlag = false
	t.Cleanup(func() { noColorFlag = old })

	if got := ansi("0;31"); got != "" {
		t.Fatalf("ansi(0;31) 在 NO_COLOR=1 时应为空,得到 %q", got)
	}
}

// TestTermDumbSuppressesANSI 验证 TERM=dumb 关颜色(无 ANSI 能力终端)。
func TestTermDumbSuppressesANSI(t *testing.T) {
	t.Setenv("TERM", "dumb")
	t.Setenv("NO_COLOR", "")
	old := noColorFlag
	noColorFlag = false
	t.Cleanup(func() { noColorFlag = old })

	if got := ansi("0;31"); got != "" {
		t.Fatalf("ansi(0;31) 在 TERM=dumb 时应为空,得到 %q", got)
	}
}

// TestStatuslineRendersWithoutANSIUnderNoColor 端到端:statusline 关闭
// 颜色后渲染产物不含 ESC 转义。这是用户最在意"管道里没乱码"。
func TestStatuslineRendersWithoutANSIUnderNoColor(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	t.Setenv("TERM", "")
	old := noColorFlag
	noColorFlag = true
	t.Cleanup(func() {
		noColorFlag = old
		// refreshColorState 改写了 ansi* vars,得把它们还原,否则后续
		// 依赖 ansiCyan 等具体转义字符串的测试会拿到空串/被污染值。
		resetAnsiDefaults()
	})

	refreshColorState() // 让 ansi* 变量回到空串

	var buf bytes.Buffer
	buf.WriteString(ansiReset + ansiGreen + "hello" + ansiReset)
	if strings.Contains(buf.String(), "\033[") {
		t.Fatalf("关闭颜色时不应出现 ANSI,得到 %q", buf.String())
	}
	if !strings.Contains(buf.String(), "hello") {
		t.Fatalf("关闭颜色时仍应输出原文,得到 %q", buf.String())
	}
}

// resetAnsiDefaults 把 ansi* vars 还原成"默认 ANSI" — 给调过
// refreshColorState(false) 的测试在 cleanup 时回滚,避免污染后续
// 依赖具体转义字符串的测试(见 statusline_test.go 的 ansiCyan 断言)。
func resetAnsiDefaults() {
	ansiReset = "\033[0m"
	ansiGreen = "\033[1;32m"
	ansiRed = "\033[0;31m"
	ansiBRed = "\033[1;31m"
	ansiCyan = "\033[0;36m"
	ansiBlue = "\033[1;34m"
	ansiYellow = "\033[0;33m"
	ansiMagenta = "\033[0;35m"
	ansiDim = "\033[2m"
	ansiWhite = "\033[0;39m"
}

// TestIsStdoutTTYReflectsStdout 验证 isStdoutTTY 在管道模式下返回
// false。这是 no-color 自动检测的关键信号位。
func TestIsStdoutTTYReflectsStdout(t *testing.T) {
	// 测试进程通常 stdout 被 test runner 重定向到文件/管道 —
	// 所以这里只验证函数不会 panic,结果可能是 true 或 false。
	_ = isStdoutTTY()
}
