//go:build windows

package cache

import (
	"os"
	"time"
)

// replaceFile 原子替换目标文件。
//
// Windows 上没有 rename(2) 那种"替换正在被打开的文件"的语义:Go 打开文件
// 走 CreateFile 且只带 FILE_SHARE_READ|FILE_SHARE_WRITE,读者(每 30s 跑一次
// 的 statusline)还没关闭时,MoveFileEx(REPLACE_EXISTING) 会以
// sharing violation 失败。读者持有文件的时间是微秒级,所以退避重试几次
// 就够,而不是把这次刷新丢掉。
func replaceFile(tmpPath, path string) error {
	const attempts = 10
	var err error
	for i := 0; i < attempts; i++ {
		if err = os.Rename(tmpPath, path); err == nil {
			return nil
		}
		time.Sleep(time.Duration(i+1) * 10 * time.Millisecond)
	}
	return err
}
