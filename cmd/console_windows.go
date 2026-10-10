//go:build windows

package cmd

import (
	"os"

	"golang.org/x/sys/windows"
)

// consoleHasVT 记录 stdout 控制台是否已开启 ANSI 转义处理(见 prepareConsole)。
var consoleHasVT bool

// prepareConsole 让 Windows 控制台能正确显示 submon 的输出:
//
//  1. 输出代码页切 UTF-8 —— UI 文案全是中文,默认 936/437 会显示乱码;
//     输入代码页一起切,`submon login` 才能读进中文标签。
//  2. 打开 ENABLE_VIRTUAL_TERMINAL_PROCESSING,ANSI 颜色才会被解析成颜色
//     而不是打印原始转义序列(Windows 10 1511+;更老的系统调用失败,
//     consoleSupportsANSI 会返回 false,颜色自动关闭)。
//
// 只对"stdout 是控制台"的场景动手:输出被重定向(Claude Code 状态栏、
// 管道、日志)时改全局代码页没有意义,还会影响调用方。
func prepareConsole() {
	if !isStdoutTTY() {
		return
	}
	_ = windows.SetConsoleOutputCP(65001)
	_ = windows.SetConsoleCP(65001)

	handle := windows.Handle(os.Stdout.Fd())
	var mode uint32
	if err := windows.GetConsoleMode(handle, &mode); err != nil {
		return
	}
	if mode&windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING != 0 {
		consoleHasVT = true
		return
	}
	if err := windows.SetConsoleMode(handle, mode|windows.ENABLE_VIRTUAL_TERMINAL_PROCESSING); err == nil {
		consoleHasVT = true
	}
}

// consoleSupportsANSI:Windows 上"是控制台"不等于"认识 ANSI",必须真的
// 打开过 VT 处理才上色,否则用户看到的是一堆 ESC 序列。
func consoleSupportsANSI() bool { return consoleHasVT }
