package statusline

import (
	"path/filepath"
	"testing"
)

// isolateAppDirs 把 submon 的配置 / 缓存 / 数据目录全部指到临时目录,测试
// 结束自动还原。
//
// 三个平台的环境变量都要设:POSIX 读 XDG_*,Windows 读 %AppData% /
// %LocalAppData%(os.UserConfigDir / os.UserCacheDir 在 Windows 上就是这么
// 解析的)。只设 XDG_* 的话,Windows 上跑 `go test ./...` 会写进开发者
// 真实的配置目录 —— 测试污染真实配置是绝不能接受的。
func isolateAppDirs(t *testing.T, dir string) {
	t.Helper()
	cache := filepath.Join(dir, "cache")
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("XDG_CACHE_HOME", cache)
	t.Setenv("XDG_DATA_HOME", filepath.Join(dir, "data"))
	t.Setenv("AppData", dir)
	t.Setenv("LocalAppData", cache)
	t.Setenv("HOME", dir)        // macOS / 不识别 XDG 的平台兜底
	t.Setenv("USERPROFILE", dir) // Windows 上的家目录兜底
}
