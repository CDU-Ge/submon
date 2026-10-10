package cmd

import (
	"os"
)

// 颜色禁用规则(no-color.org 标准 + 终端常识):
//
//	1. --no-color flag(用户显式关)
//	2. NO_COLOR 环境变量非空(标准 no-color.org)
//	3. TERM=dumb(没有 ANSI 能力的终端)
//	4. stdout 不是 TTY(管道 / 重定向时 — 否则脚本/CI 日志被 ANSI 转义污染)
//	5. 终端不支持 ANSI(Windows 且 VT 处理打不开,见 consoleSupportsANSI;
//	   只对"stdout 真是终端"的情况生效,管道接走时由第 4 条决定)
//
// 全部命中"开",任一命中即关。Force 优先级最低,留给 FORCE_COLOR(暂未实现)。
//
// 例外:allowPipeColor 为 true 时跳过第 4 条 — statusline 的唯一消费者是
// Claude Code,它经管道捕获输出并渲染 ANSI,非 TTY 在此不代表"无人看色"。

var (
	noColorFlag    bool
	allowPipeColor bool
)

func init() {
	// rootCmd 在 cmd/root.go 声明;此处 init 顺序在 root.go 之后(Golang 同包
	// 按文件名字典序;为保险起见直接 attach PersistentFlags)。
	rootCmd.PersistentFlags().BoolVar(&noColorFlag, "no-color", false,
		"禁用 ANSI 颜色输出(NO_COLOR / TERM=dumb / 非 TTY 时自动禁用)")
}

// useColor 全局判断是否输出 ANSI 颜色。每次都查 — noColorFlag 由 cobra
// 解析 argv 后才赋值,缓存层无法简单处理 flag→true 的翻转。环境检查
// 代价都是 syscall(stat + getenv),单次 < 1µs,足以承载每个 statusline
// 帧的调用频率(每分钟最多几次)。
func useColor() bool {
	switch {
	case noColorFlag:
		return false
	case os.Getenv("NO_COLOR") != "":
		return false
	case os.Getenv("TERM") == "dumb":
		return false
	}
	if !isStdoutTTY() {
		// 管道 / 重定向:默认关色(否则脚本、CI 日志被 ANSI 污染),
		// 只有 statusline 例外 —— 它的消费者是 Claude Code,自己会渲染 ANSI。
		return allowPipeColor
	}
	// stdout 是终端,再问终端本身认不认 ANSI。Windows 的 conhost 默认不认,
	// 要看 prepareConsole 有没有把 VT 处理打开(见 console_windows.go);
	// POSIX 恒为 true。注意这一条只对"真终端"生效:输出被管道接走时
	// 终端能力与染色无关,由上面的 allowPipeColor 决定。
	return consoleSupportsANSI()
}

// ansi 返回 ANSI 转义序列前缀(末尾加 'm');未启用颜色时返回空串。
// 调用方可直接拼接:ansi("0;31") + "red text" + ansi("0")。
func ansi(code string) string {
	if !useColor() {
		return ""
	}
	return "\033[" + code + "m"
}

// isStdoutTTY 单独拎出方便测试时 mock(目前实现仅读 stdout fd)。
func isStdoutTTY() bool {
	fi, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}
