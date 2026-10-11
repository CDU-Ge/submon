package cmd

import (
	"os"
	"path/filepath"
	"sync"
)

// 内核锁非阻塞,进程退出自动释放。锁文件不能删除或按年龄替换:
// 删除会使并发调用锁住不同文件,从而失去互斥。
func tryStatuslineLock(path string) (func(), bool) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return func() {}, false
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return func() {}, false
	}
	if err := lockStatuslineFile(f); err != nil {
		_ = f.Close()
		return func() {}, false
	}
	var once sync.Once
	return func() { once.Do(func() { _ = f.Close() }) }, true
}
