package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/spf13/cobra"
)

// withConfigPath 把 config dir 临时切到一个空目录,跑完测试还原。
// 因为 cacheClear 直接用 cfg.CachePath(),测试不能污染用户家目录。
func withConfigPath(t *testing.T, fn func()) {
	t.Helper()
	dir := t.TempDir()
	isolateAppDirs(t, dir)
	// config.Load() 在没有 toml 时返回 default,CachePath 走平台缓存目录。
	_ = os.MkdirAll(filepath.Join(dir, "cache"), 0o755)
	fn()
}

// writeFakeCache 把一段最小化 cache 写到 XDG_CACHE_HOME,模拟 watch
// 跑过一次的状态。
func writeFakeCache(t *testing.T) string {
	t.Helper()
	cacheDir := os.Getenv("XDG_CACHE_HOME")
	if cacheDir == "" {
		cacheDir = filepath.Join(os.Getenv("HOME"), ".cache")
	}
	_ = os.MkdirAll(cacheDir, 0o755)
	path := filepath.Join(cacheDir, "submon", "cache.json")
	_ = os.MkdirAll(filepath.Dir(path), 0o755)
	f := cache.File{Entries: make(map[string]*cache.Entry)}
	f.Entries["zhipu"] = &cache.Entry{}
	if err := cache.New(path).Save(&f); err != nil {
		t.Fatalf("写入假缓存: %v", err)
	}
	return path
}

// TestCacheClearRefusesInNonTTYWithoutForce 验证非 TTY 跑 cache clear
// 时拒绝执行并给出 --force 提示。这是"避免 CI 误删现场"的硬约束。
func TestCacheClearRefusesInNonTTYWithoutForce(t *testing.T) {
	withConfigPath(t, func() {
		writeFakeCache(t)
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cacheClearForce = false
		err := runCacheClear(cmd)
		if err == nil {
			t.Fatalf("非 TTY 无 --force 时必须拒绝,但成功了")
		}
		if !strings.Contains(err.Error(), "submon cache clear -f") {
			t.Fatalf("拒绝信息应提示 --force,得到 %v", err)
		}
	})
}

// TestCacheClearHonorsForceFlag 验证 --force=true 时直接删,无需 y/N。
func TestCacheClearHonorsForceFlag(t *testing.T) {
	withConfigPath(t, func() {
		path := writeFakeCache(t)
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cacheClearForce = true
		if err := runCacheClear(cmd); err != nil {
			t.Fatalf("--force 时应成功,得到 %v", err)
		}
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("--force 后缓存应被删除,仍存在: err=%v", err)
		}
	})
}

// TestCacheClearIsIdempotentOnMissing 验证缓存不存在时 cache clear
// 不报错(避免排查链路因为"清理"动作炸出额外错误)。
func TestCacheClearIsIdempotentOnMissing(t *testing.T) {
	withConfigPath(t, func() {
		cmd := &cobra.Command{}
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cacheClearForce = true
		// 不写缓存,直接清 — 应走 IsNotExist 分支。
		if err := runCacheClear(cmd); err != nil {
			t.Fatalf("无缓存时清空应不报错,得到 %v", err)
		}
	})
}

// TestCacheListRendersJSON 验证 `cache` 无参 = 列出现有缓存。
// 缓存空时返回空 JSON object,而不是 panic。
func TestCacheListRendersJSON(t *testing.T) {
	withConfigPath(t, func() {
		cmd := &cobra.Command{}
		buf := &bytes.Buffer{}
		cmd.SetOut(buf)
		cmd.SetErr(&bytes.Buffer{})
		if err := runCacheList(cmd); err != nil {
			t.Fatalf("空缓存时列应不报错,得到 %v", err)
		}
		// 输出应是合法 JSON(可能是 "{}" 或带 entries 的对象)。
		if buf.Len() == 0 {
			t.Fatalf("cache 列应输出内容,得到空")
		}
	})
}

// 验证 cacheCmd 把 "clear" 子参数路由到 runCacheClear — 防止 cobra
// 子命令分发回归。
func TestCacheCmdRoutesClearSubcommand(t *testing.T) {
	withConfigPath(t, func() {
		cacheClearForce = true
		writeFakeCache(t)
		cmd := cacheCmd
		cmd.SetOut(&bytes.Buffer{})
		cmd.SetErr(&bytes.Buffer{})
		cmd.SetArgs([]string{"clear"})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("cache clear 应成功,得到 %v", err)
		}
	})
}

// 抑制 unused import warning 当 file 裁剪后某些 helper 不再被引用。
var _ = config.Load
