//go:build windows

package cmd

import "os"

// termSignals:Windows 没有 SIGTERM,进程只能收到 Ctrl-C / Ctrl-Break
// (两者在 Go 里都是 os.Interrupt)。任务计划程序结束任务时用的是
// TerminateProcess,不经过信号,所以这里不需要别的。
func termSignals() []os.Signal { return []os.Signal{os.Interrupt} }
