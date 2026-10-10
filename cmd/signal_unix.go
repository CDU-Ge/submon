//go:build !windows

package cmd

import (
	"os"
	"syscall"
)

// termSignals 返回"请求正常退出"的信号集合:POSIX 上 Ctrl-C(SIGINT)与
// SIGTERM(默认 kill、systemd 停止单元)都算。
func termSignals() []os.Signal { return []os.Signal{os.Interrupt, syscall.SIGTERM} }
