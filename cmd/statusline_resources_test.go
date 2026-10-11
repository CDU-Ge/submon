package cmd

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

func TestQuotaRenderingDoesNotRefresh(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	path, _ := cfg.CachePath()
	for i := 0; i < 20; i++ {
		got := quotaSegmentForProvider(cfg, model.Provider("zhipu"), time.Now())
		if !strings.Contains(got, "暂无数据") || strings.Contains(got, "刷新中") {
			t.Fatalf("缺失缓存必须诚实展示而不启动刷新: %q", got)
		}
	}
	if _, err := os.Stat(filepath.Dir(path)); !os.IsNotExist(err) {
		t.Fatalf("配额渲染创建了文件/刷新锁: %v", err)
	}
}

func TestStatuslineLockCannotBeStolenByAge(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git.lock")
	unlock, ok := tryStatuslineLock(path)
	if !ok {
		t.Fatal("首次加锁失败")
	}
	defer unlock()
	old := time.Now().Add(-time.Hour)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
	if release, ok := tryStatuslineLock(path); ok {
		release()
		t.Fatal("活跃锁不应因文件年龄被抢占")
	}
	unlock()
	release, ok := tryStatuslineLock(path)
	if !ok {
		t.Fatal("释放后应能重新加锁")
	}
	release()
}

func TestGitFailureIsCachedAndKeepsLastBranch(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	cwd := t.TempDir() // 非仓库:git 查询失败。
	now := time.Now()
	path := gitCachePath(cfg, cwd)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(gitState{Branch: "previous", Dirty: true, At: now.Add(-time.Hour)})
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if branch, dirty := gitStatusCached(cfg, cwd, now); branch != "previous" || !dirty {
		t.Fatalf("失败时应保留旧值: %q %v", branch, dirty)
	}
	st, ok := loadGitState(path)
	if !ok || !st.At.Equal(now) {
		t.Fatal("失败尝试没有节流:下一帧将再次启动 git")
	}
}

// TestMain 将测试二进制兼作可控 Git,避免依赖 shell,Windows 也能运行。
func TestMain(m *testing.M) {
	if os.Getenv("SUBMON_TEST_STATUSLINE") == "1" {
		Execute()
		os.Exit(0)
	}
	if os.Getenv("SUBMON_TEST_JOB_PARENT") == "1" {
		_ = os.Unsetenv("SUBMON_TEST_JOB_PARENT")
		exe, err := os.Executable()
		if err != nil {
			os.Exit(2)
		}
		c := exec.Command(exe)
		c.SysProcAttr = hideWindowProcAttr()
		if _, err := gitOutput(c); err != nil {
			os.Exit(3)
		}
		os.Exit(0)
	}
	if path := os.Getenv("SUBMON_TEST_LOCK"); path != "" {
		unlock, ok := tryStatuslineLock(path)
		if !ok {
			os.Exit(2)
		}
		defer unlock()
		_, _ = os.Stdout.WriteString("ready")
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	if logPath := os.Getenv("SUBMON_TEST_SLOW_GIT"); logPath != "" {
		f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
		if err != nil {
			os.Exit(2)
		}
		_, _ = f.WriteString("spawn\n")
		_ = f.Close()
		if pidPath := os.Getenv("SUBMON_TEST_GIT_PID_FILE"); pidPath != "" {
			_ = os.WriteFile(pidPath, []byte(strconv.Itoa(os.Getpid())), 0600)
		}
		time.Sleep(10 * time.Second)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestGitTimeoutAndThrottle(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	dir := t.TempDir()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(exe)
	if err != nil {
		t.Fatal(err)
	}
	name := "git"
	if filepath.Ext(exe) == ".exe" {
		name += ".exe"
	}
	if err := os.WriteFile(filepath.Join(dir, name), raw, 0700); err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "spawns")
	t.Setenv("PATH", dir)
	t.Setenv("SUBMON_TEST_SLOW_GIT", logPath)
	cwd := t.TempDir()
	start := time.Now()
	gitStatusCached(cfg, cwd, start)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Fatalf("卡住的 git 没有按预算终止: %v", elapsed)
	}
	for i := 0; i < 100; i++ {
		gitStatusCached(cfg, cwd, start.Add(time.Second))
	}
	logged, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(logged) != "spawn\n" {
		t.Fatalf("超时后高频调用又拉起 git: %q", logged)
	}
}

func TestStdinReaderClosesAfterCompleteJSON(t *testing.T) {
	pr, pw := io.Pipe()
	defer pw.Close()
	go func() { _, _ = pw.Write([]byte(`{"session_id":"test"}`)) }()
	if got := readStdinWithin(pr, time.Second); string(got) != `{"session_id":"test"}` {
		t.Fatalf("payload = %q", got)
	}
	done := make(chan error, 1)
	go func() { _, err := pw.Write([]byte("more")); done <- err }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("返回后 reader 仍在读取")
		}
	case <-time.After(time.Second):
		_ = pr.Close()
		t.Fatal("返回后 reader 未关闭")
	}
}

func TestStatuslineLockReleasedWhenProcessDies(t *testing.T) {
	path := filepath.Join(t.TempDir(), "git.lock")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	c := exec.CommandContext(ctx, exe)
	c.Env = append(os.Environ(), "SUBMON_TEST_LOCK="+path)
	out, err := c.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Process.Kill(); _ = c.Wait() }()
	ready := make([]byte, 5)
	if _, err := io.ReadFull(out, ready); err != nil {
		t.Fatal(err)
	}
	if release, ok := tryStatuslineLock(path); ok {
		release()
		t.Fatal("其他进程持锁期间不应取得锁")
	}
	_ = c.Process.Kill()
	_ = c.Wait()
	release, ok := tryStatuslineLock(path)
	if !ok {
		t.Fatal("进程终止后内核锁未释放")
	}
	release()
}

func TestStdinInputMemoryBound(t *testing.T) {
	raw := readStdinWithin(strings.NewReader(strings.Repeat("x", 2<<20)), time.Second)
	if len(raw) != 1<<20 {
		t.Fatalf("输入应限制到 1MiB,得到 %d bytes", len(raw))
	}
}

func TestExpiredQuotaIsReadOnlyInEveryLayout(t *testing.T) {
	t.Setenv("SUBMON_SHOW_PROVIDER", "1")
	isolateAppDirs(t, t.TempDir())
	t.Setenv("SUBMON_LAYOUT", "")
	cfg := config.Default()
	path, _ := cfg.CachePath()
	now := time.Now()
	remaining := 90.0
	file := &cache.File{Entries: map[string]*cache.Entry{
		"zhipu": {Snapshot: &model.Snapshot{
			Provider: model.ProviderZhipu, FetchedAt: now.Add(-time.Hour),
			ShortWindow: &model.Window{Label: "5h", RemainingPct: &remaining},
		}},
	}}
	raw, err := json.Marshal(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "https://open.bigmodel.cn/api/anthropic")
	for _, layout := range []string{"a", "b", "c"} {
		cfg.Statusline.Layout = layout
		for i := 0; i < 10; i++ {
			line := renderStatusline(&statuslineInput{}, cfg, now)
			if !strings.Contains(line, "缓存过期") || strings.Contains(line, "刷新中") || strings.Contains(line, "ZHIPU") {
				t.Fatalf("layout %s: %q", layout, line)
			}
		}
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(raw, after) {
		t.Fatal("渲染改变了配额缓存")
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("渲染创建了刷新锁等文件: %v", entries)
	}
}

func TestStatuslineHasNoProviderFlags(t *testing.T) {
	for _, name := range []string{"provider", "show-provider"} {
		if statuslineCmd.Flags().Lookup(name) != nil {
			t.Errorf("statusline 仍暴露 --%s", name)
		}
	}
}

func TestStatuslineTimingKeepsStdoutUnchanged(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	t.Setenv("SUBMON_TEST_STATUSLINE", "1")
	t.Setenv("ANTHROPIC_BASE_URL", "")
	t.Setenv("SUBMON_STATUSLINE_TIMING", "0")
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	run := func() (string, string) {
		c := exec.Command(exe, "statusline", "--no-color")
		c.Stdin = strings.NewReader(`{"model":{"display_name":"Claude"}}`)
		var stdout, stderr bytes.Buffer
		c.Stdout, c.Stderr = &stdout, &stderr
		if err := c.Run(); err != nil {
			t.Fatalf("运行失败: %v %s", err, stderr.String())
		}
		return stdout.String(), stderr.String()
	}
	baseline, stderr := run()
	if stderr != "" {
		t.Fatalf("默认产生了诊断日志: %q", stderr)
	}
	t.Setenv("SUBMON_STATUSLINE_TIMING", "1")
	traced, stderr := run()
	if baseline == "" || traced != baseline {
		t.Fatalf("诊断改变输出: %q != %q", traced, baseline)
	}
	var stages map[string]float64
	if err := json.Unmarshal([]byte(strings.TrimPrefix(strings.TrimSpace(stderr), "SUBMON_STATUSLINE_TIMING ")), &stages); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"run_ms", "cli_ms", "config_ms", "stdin_ms", "render_ms"} {
		if _, ok := stages[name]; !ok {
			t.Errorf("缺少分段计时 %s", name)
		}
	}
}
