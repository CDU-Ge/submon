//go:build !windows

package cache

import "os"

// replaceFile 原子替换目标文件:POSIX 的 rename(2) 语义本身就是原子的,
// 且不会因为别的进程正打开目标文件而失败 —— 一次调用即可。
func replaceFile(tmpPath, path string) error { return os.Rename(tmpPath, path) }
