package statusline

import (
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

// gitRun 在 dir 里跑一条 git 命令,失败即测试失败。身份与"不签名"显式
// 给定,免读用户全局配置,结果可复现。
func gitRun(t *testing.T, dir string, args ...string) string {
	t.Helper()
	c := exec.Command("git", append([]string{"-C", dir, "-c", "commit.gpgsign=false"}, args...)...)
	c.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=submon test", "GIT_AUTHOR_EMAIL=test@example.invalid",
		"GIT_COMMITTER_NAME=submon test", "GIT_COMMITTER_EMAIL=test@example.invalid",
	)
	out, err := c.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v 失败: %v\n%s", args, err, out)
	}
	return string(out)
}

// gitFixtureRepo 造一个临时仓库:分支 main、一次提交、工作区干净。
func gitFixtureRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git 不在 PATH,跳过: %v", err)
	}
	dir := t.TempDir()
	gitRun(t, dir, "init")
	gitRun(t, dir, "symbolic-ref", "HEAD", "refs/heads/main")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	gitRun(t, dir, "add", "a.txt")
	gitRun(t, dir, "commit", "-m", "init")
	return dir
}

func TestStripANSI(t *testing.T) {
	for in, want := range map[string]string{
		"GLM-5.3":          "GLM-5.3",
		"GLM-5.2[1m]":      "GLM-5.2",
		"\033[1mHi\033[0m": "Hi",
		"":                 "",
	} {
		if got := stripANSI(in); got != want {
			t.Errorf("stripANSI(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRenderStatuslineFull(t *testing.T) {
	cfg := config.Default()
	in := &statuslineInput{}
	// cwd 用临时目录:以前写死作者机器的 ~/Projects/subscription-monitor,
	// 换台机器(尤其 Windows)整个用例就红。分支段单独由
	// TestRenderStatuslineBranchSegment 覆盖。
	dir := t.TempDir()
	isolateAppDirs(t, t.TempDir()) // git 缓存要落在隔离目录,不污染真实缓存
	in.Workspace.CurrentDir = dir
	in.Model.Display = "GLM-5.3[1m]"
	in.Effort.Level = "xhigh"
	in.FastMode = true
	used, total, pctx := 12345.0, 200000.0, 6.0
	in.ContextWindow = contextWindowInfo{TotalInputTokens: &used, ContextWindowSize: &total, UsedPercentage: &pctx}

	// Quota segment reads the real cache path; absent data degrades to a
	// hint, which still proves the rest of the line renders.
	line := renderStatusline(in, cfg, time.Now())
	for _, want := range []string{filepath.Base(dir), "GLM-5.3", "200k", "[", "]", "6%", "xhigh", "fast"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %s", want, line)
		}
	}
	// 模型与 effort 紧贴(中间无段间双空格)。
	if !strings.Contains(line, "GLM-5.3"+ansiReset+" "+ansiMagenta+"xhigh") {
		t.Errorf("model and effort should be tightly packed: %s", line)
	}
	if strings.Contains(line, "[1m") {
		t.Errorf("model name ANSI residue leaked: %s", line)
	}

	// 减掉的旧符号不应再出现。
	for _, banned := range []string{"➜", "⚡", "✗", "⎇", "git:(", "left"} {
		if strings.Contains(line, banned) {
			t.Errorf("banned token %q leaked into line: %s", banned, line)
		}
	}
}

// TestReadStdinWithin 锁住"绝不无限期等待 stdin"的契约:旧实现 io.ReadAll
// 在 Claude Code 写完 JSON 却不关管道时会永久阻塞 —— 状态栏一行都不出。
func TestReadStdinWithin(t *testing.T) {
	// 1. 有数据 + EOF:原样返回。
	const payload = `{"model":{"display_name":"GLM-5.3"}}`
	if got := readStdinWithin(strings.NewReader(payload), 200*time.Millisecond); string(got) != payload {
		t.Errorf("完整输入应原样返回,得到 %q", got)
	}

	// 2. 有数据但管道不关:期限内返回已读到的字节(payload 不能丢)。
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	go func() { _, _ = pw.Write([]byte(`{"model":`)) }()
	start := time.Now()
	got := readStdinWithin(pr, 150*time.Millisecond)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("管道不关时应按期限返回,实际等了 %v", elapsed)
	}
	if string(got) != `{"model":` {
		t.Errorf("应返回已读到的 payload,得到 %q", got)
	}

	// 3. 一个字都没有且管道不关:期限内返回空,绝不挂死。
	pr2, pw2 := io.Pipe()
	t.Cleanup(func() { _ = pw2.Close(); _ = pr2.Close() })
	start = time.Now()
	if got := readStdinWithin(pr2, 100*time.Millisecond); len(got) != 0 {
		t.Errorf("无输入应返回空,得到 %q", got)
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("无输入时也应按期限返回,实际等了 %v", elapsed)
	}
}

// TestReadStdinWithinReturnsEarlyOnCompletePayload 锁住"payload 自洽就立刻返回":
// Claude Code 写完 JSON 不一定关管道,旧实现会白等满预算 —— 而 CC 会取消还在
// 跑的状态栏脚本,那一帧整行空白,也是一种跳闪。
func TestReadStdinWithinReturnsEarlyOnCompletePayload(t *testing.T) {
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close(); _ = pr.Close() })
	const payload = `{"model":{"display_name":"GLM-5.3"},"context_window":{"used_percentage":42}}`
	go func() { _, _ = pw.Write([]byte(payload)) }()

	start := time.Now()
	got := readStdinWithin(pr, 5*time.Second)
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("payload 完整后应立即返回,实际等了 %v", elapsed)
	}
	if string(got) != payload {
		t.Errorf("应原样返回完整 payload,得到 %q", got)
	}
}

// TestCtxUsagePrefersUsedPercentage: used_percentage 是 Claude Code 自己算好的
// 值(与 /context 同源);token 比值只是字段缺失时的兜底。
func TestCtxUsagePrefersUsedPercentage(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	now := time.Now()

	in := &statuslineInput{SessionID: "sess-pref"}
	used, total, pct := 0.0, 200000.0, 62.0
	in.ContextWindow = contextWindowInfo{TotalInputTokens: &used, ContextWindowSize: &total, UsedPercentage: &pct}
	if p, _, ok := ctxUsage(in, cfg, now); !ok || p != 62 {
		t.Errorf("应优先用 used_percentage,得到 (%v, ok=%v)", p, ok)
	}

	// 没有 used_percentage 时才回落到 token 比值。
	in2 := &statuslineInput{SessionID: "sess-tokens"}
	u2, t2 := 50000.0, 200000.0
	in2.ContextWindow = contextWindowInfo{TotalInputTokens: &u2, ContextWindowSize: &t2}
	if p, _, ok := ctxUsage(in2, cfg, now.Add(time.Second)); !ok || p != 25 {
		t.Errorf("缺 used_percentage 时应按 token 比值,得到 (%v, ok=%v)", p, ok)
	}
}

// TestCtxUsageSurvivesTransientZero 是这次跳闪的回归测试:官方文档明确
// total_input_tokens / used_percentage 在首次响应之前和 /compact 之后到下一次
// 响应之间都是 0 —— 那是"这一帧没有数据",状态栏不能跟着归零再跳回来。
func TestCtxUsageSurvivesTransientZero(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	now := time.Now()

	in := &statuslineInput{SessionID: "sess-1"}
	used, total, pct := 80000.0, 200000.0, 40.0
	in.ContextWindow = contextWindowInfo{TotalInputTokens: &used, ContextWindowSize: &total, UsedPercentage: &pct}
	if p, eff, ok := ctxUsage(in, cfg, now); !ok || p != 40 || eff != 200000 {
		t.Fatalf("正常帧 = (%v, %v, ok=%v), want (40, 200000, true)", p, eff, ok)
	}

	zero := &statuslineInput{SessionID: "sess-1"}
	z := 0.0
	zero.ContextWindow = contextWindowInfo{TotalInputTokens: &z, ContextWindowSize: &total, UsedPercentage: &z}
	if p, _, ok := ctxUsage(zero, cfg, now.Add(time.Second)); !ok || p < 39 {
		t.Errorf("瞬时归零应沿用上一帧,得到 (%v, ok=%v)", p, ok)
	}

	// 保护窗口过后必须真的归零:/clear 之后不能永远显示旧值。
	if p, _, _ := ctxUsage(zero, cfg, now.Add(ctxStickyTTL+time.Second)); p != 0 {
		t.Errorf("超过 %s 后应显示真实归零,得到 %v", ctxStickyTTL, p)
	}

	// 快照按会话隔离:别的会话不能被这个会话的旧值污染。
	other := &statuslineInput{SessionID: "sess-2"}
	other.ContextWindow = zero.ContextWindow
	if p, _, _ := ctxUsage(other, cfg, now.Add(time.Second)); p != 0 {
		t.Errorf("不同 session 不应共用快照,得到 %v", p)
	}
}

// TestRenderStatuslineNoZeroFlicker 三套排版都要对齐:空窗帧不渲染 0%。
func TestRenderStatuslineNoZeroFlicker(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	now := time.Now()

	good := &statuslineInput{SessionID: "sess-render"}
	used, total, pct := 120000.0, 200000.0, 60.0
	good.ContextWindow = contextWindowInfo{TotalInputTokens: &used, ContextWindowSize: &total, UsedPercentage: &pct}

	zero := &statuslineInput{SessionID: "sess-render"}
	z := 0.0
	zero.ContextWindow = contextWindowInfo{TotalInputTokens: &z, ContextWindowSize: &total, UsedPercentage: &z}

	for _, layout := range []string{"a", "b", "c"} {
		cfg.Statusline.Layout = layout
		if line := renderStatusline(good, cfg, now); !strings.Contains(line, "60%") {
			t.Fatalf("layout %s 正常帧应显示 60%%: %s", layout, line)
		}
		// 归零帧必须仍是上一帧的真实值(60%),而不是跳到 0%。
		if line := renderStatusline(zero, cfg, now.Add(time.Second)); !strings.Contains(line, "60%") {
			t.Errorf("layout %s 在瞬时归零帧丢了上下文值: %s", layout, line)
		}
	}
}

// TestRenderStatuslineDoesNotInventDir 守住"不拿进程工作目录冒充会话目录"
// 的契约:手动跑 `echo '{"model":...}' | submon statusline` 时,payload 里
// 没有 workspace.current_dir,以前会退回 os.Getwd() 显示 exe 所在目录名
// (例如二进制在 E:\Tools 就显示 "Tools")—— 凭空出现的段,既误导用户,
// 又白 spawn 一次 git。
func TestRenderStatuslineDoesNotInventDir(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()

	base := ""
	if wd, err := os.Getwd(); err == nil {
		base = filepath.Base(wd)
	}

	for _, layout := range []string{"a", "b", "c"} {
		cfg.Statusline.Layout = layout
		in := &statuslineInput{}
		in.Model.Display = "GLM-5.3"
		line := stripANSI(renderStatusline(in, cfg, time.Now()))
		if base != "" && strings.Contains(line, base) {
			t.Errorf("layout %s 把进程工作目录 %q 当成会话目录渲染: %s", layout, base, line)
		}
		if layout != "c" && !strings.Contains(line, "GLM-5.3") {
			t.Errorf("layout %s 丢了模型段: %s", layout, line)
		}
	}
}

// TestRenderStatuslineBranchSegment 三套排版都要渲染分支段(三处调用点
// 都走同一个单次 gitStatus)。
func TestRenderStatuslineBranchSegment(t *testing.T) {
	dir := gitFixtureRepo(t)
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	in := &statuslineInput{}
	in.Workspace.CurrentDir = dir

	for _, layout := range []string{"a", "b", "c"} {
		cfg.Statusline.Layout = layout
		t.Run("layout="+layout, func(t *testing.T) {
			if line := renderStatusline(in, cfg, time.Now()); !strings.Contains(line, "main") {
				t.Errorf("layout %s 缺分支段: %s", layout, line)
			}
		})
	}
}

// TestGitStatusTempRepo 锁住 gitStatus 的解析与脏标记语义:一次 spawn
// 同时拿分支名 + 脏标记(替代原来的 rev-parse + diff --quiet 两次)。
func TestGitStatusTempRepo(t *testing.T) {
	dir := gitFixtureRepo(t)

	branch, dirty, err := gitStatus(dir)
	if err != nil {
		t.Fatalf("gitStatus(仓库) 报错: %v", err)
	}
	if branch != "main" || dirty {
		t.Fatalf("干净仓库 = (%q, dirty=%v), want (\"main\", false)", branch, dirty)
	}

	// untracked 不算脏:构建产物/临时文件不该点亮 *(与 git diff --quiet
	// HEAD 的语义一致)。
	if err := os.WriteFile(filepath.Join(dir, "build.out"), []byte("x\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, dirty, _ := gitStatus(dir); dirty {
		t.Errorf("untracked 文件不应判为脏")
	}

	// 跟踪文件的改动才算脏。
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, dirty, _ := gitStatus(dir); !dirty {
		t.Errorf("跟踪文件被改动应判为脏")
	}

	// detached HEAD:与 rev-parse --abbrev-ref 的历史输出对齐("HEAD")。
	gitRun(t, dir, "checkout", "--detach")
	if branch, _, _ := gitStatus(dir); branch != "HEAD" {
		t.Errorf("detached HEAD 分支名 = %q, want \"HEAD\"", branch)
	}

	// 非仓库目录:返回 error,渲染层据此整段跳过。
	if _, _, err := gitStatus(t.TempDir()); err == nil {
		t.Errorf("非 git 目录应报错")
	}
}

func TestEffortLabel(t *testing.T) {
	cases := map[string]string{
		"":       "",    // 空 effort 不显示
		"low":    "low", // 所有档都显示
		"medium": "medium",
		"high":   "high",
		"xhigh":  "xhigh",
		"max":    "max",
	}
	for level, want := range cases {
		in := &statuslineInput{}
		in.Effort.Level = level
		out := effortLabel(in)
		if want == "" {
			if out != "" {
				t.Errorf("effort=%q should produce empty label, got %q", level, out)
			}
			continue
		}
		if !strings.Contains(out, want) {
			t.Errorf("effort=%q should contain %q, got %q", level, want, out)
		}
	}
}

func TestFastLabel(t *testing.T) {
	if out := fastLabel(&statuslineInput{FastMode: false}); out != "" {
		t.Errorf("fast=false should produce empty label, got %q", out)
	}
	out := fastLabel(&statuslineInput{FastMode: true})
	if !strings.Contains(out, "fast") {
		t.Errorf("fast=true should contain 'fast', got %q", out)
	}
	if strings.Contains(out, "⚡") {
		t.Errorf("fast label should not contain ⚡ emoji: %q", out)
	}
}

func TestGitInlineSegment(t *testing.T) {
	isolateAppDirs(t, t.TempDir())
	branch, _, err := gitStatus(".")
	if err != nil {
		t.Skipf("not in a git repo: %v", err)
	}
	cfg := config.Default()
	gotBranch, _ := gitInlineSegment(cfg, ".", time.Now())
	if gotBranch != branch {
		t.Errorf("gitInlineSegment() branch = %q, want %q", gotBranch, branch)
	}
	// 不应有 ANSI 包装,纯文本。
	if strings.Contains(gotBranch, "\033") {
		t.Errorf("gitInlineSegment should return plain text, got %q", gotBranch)
	}
}

// TestGitStatusCachedThrottles 守住"重活节流"契约:TTL 内不再 spawn git,
// 过期才重新跑。PATH 清空后 git 不可用 —— 缓存命中时仍能拿到旧值,过期查询失败保留旧值
// 并节流,正好证明"命中=没跑 git"。
func TestGitStatusCachedThrottles(t *testing.T) {
	dir := gitFixtureRepo(t)
	isolateAppDirs(t, t.TempDir())
	cfg := config.Default()
	now := time.Now()

	if branch, dirty := gitStatusCached(cfg, dir, now); branch != "main" || dirty {
		t.Fatalf("首次 = (%q, dirty=%v), want (\"main\", false)", branch, dirty)
	}

	// 缓存必须按目录隔离:会话内换目录时,新目录不能拿到旧目录的结果
	// (曾经只用 session_id 当键,就踩了这个坑)。
	if branch, _ := gitStatusCached(cfg, t.TempDir(), now.Add(time.Second)); branch != "" {
		t.Errorf("不同目录不应共用缓存,得到 %q", branch)
	}

	// 让 git 彻底不可用。
	empty := t.TempDir()
	t.Setenv("PATH", empty)

	if branch, _ := gitStatusCached(cfg, dir, now.Add(2*time.Second)); branch != "main" {
		t.Errorf("TTL 内应命中缓存(git 已不可用),得到 %q", branch)
	}
	if branch, _ := gitStatusCached(cfg, dir, now.Add(gitStatusTTL+time.Second)); branch != "main" {
		t.Errorf("TTL 过期查询失败应保留旧值,得到 %q", branch)
	}
}

// TestStatuslineCommandShellSafe 守住写进 Claude Code 的命令行形状:必须以
// statusline 结尾、含空格的路径必须加引号(Git Bash 与 PowerShell 都按
// 空格切词)。Windows 专属的反斜杠转换见 claude_config_windows_test.go。
func TestCtxBar(t *testing.T) {
	// 8 格宽,精度 1/(8*8)=1.5625%。空格填未占用部分。
	cases := map[float64]string{
		0:    "        ",
		6:    "▍       ", // 6/100*64=3.84 → 第 1 格 ▍
		12:   "▉       ", // 12/100*64=7.68 → 第 1 格 ▉
		12.5: "█       ", // 满 1 格
		50:   "████    ",
		75:   "██████  ",
		100:  "████████",
	}
	for pct, want := range cases {
		if got := ctxBar(pct); got != want {
			t.Errorf("ctxBar(%.2f) = %q, want %q", pct, got, want)
		}
	}
	// 边界截断。
	if got := ctxBar(-10); got != "        " {
		t.Errorf("ctxBar(-10) should clamp to 0, got %q", got)
	}
	if got := ctxBar(150); got != "████████" {
		t.Errorf("ctxBar(150) should clamp to 100, got %q", got)
	}
}

func TestQuotaWindowMarkerAndBadge(t *testing.T) {
	cfg := config.Default()
	w := &model.Window{Label: "5h"}
	rem := float64(3)
	w.RemainingPct = &rem
	reset := time.Now().Add(90 * time.Minute)
	w.ResetAt = &reset

	out := colorizeWindow("5h", w, time.Now(), cfg)
	// Order: "<countdown> <pct>%" — pct after countdown (5h 形态)。
	if !strings.Contains(out, "3%") {
		t.Errorf("expected pct%% in output, got %s", out)
	}
	if strings.Contains(out, "left") {
		t.Errorf("'left' marker should be gone (冗余), got %s", out)
	}
	if !strings.Contains(out, ansiRed) { // 3% < crit threshold 5%
		t.Errorf("expected red at 3%% remaining, got %s", out)
	}

}

func TestPeakHourReminder(t *testing.T) {
	cfg := config.Default()

	// 按会话 base URL 验证 provider 识别。

	// 1. 非 Zhipu:空(其他 provider 不打提醒,这是契约)。
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.minimax.io/anthropic")
	if got := peakHourReminder(cfg, time.Now()); got != "" {
		t.Errorf("non-Zhipu should be empty, got %q", got)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "https://opencode.ai/zen")
	if got := peakHourReminder(cfg, time.Now()); got != "" {
		t.Errorf("non-Zhipu should be empty, got %q", got)
	}

	// 后续用例全部用 Zhipu。
	t.Setenv("ANTHROPIC_BASE_URL", "https://open.bigmodel.cn/api/anthropic")

	// 锚定 2024-01-15 为周一(2024-01-01 是周一,14 天后同是周一;
	// 13/14 分别落在周六/周日)。先断言这条假设,免得测试默默过期。
	mon := time.Date(2024, 1, 15, 0, 0, 0, 0, time.UTC)
	if mon.Weekday() != time.Monday {
		t.Fatalf("assumption broken: 2024-01-15 is %v, want Monday", mon.Weekday())
	}

	utc := func(ymdHM string) time.Time {
		parsed, err := time.Parse("2006-01-02 15:04", ymdHM)
		if err != nil {
			t.Fatal(err)
		}
		return parsed.UTC()
	}

	// 高峰期 = 周一至周五 14:00-18:00 (UTC+8,Asia/Shanghai)。
	// 期望:窗口外空、窗口内 "高峰 Xh Ym",剩余 < 30min 转红。

	cases := []struct {
		name      string
		utcTime   string
		wantEmpty bool
		wantColor string // "" = 不检查
	}{
		// 周六/周日 同时间点:窗口外。
		{"周六 07:00 UTC", utc("2024-01-13 07:00").Format("2006-01-02 15:04"), true, ""},
		{"周日 07:00 UTC", utc("2024-01-14 07:00").Format("2006-01-02 15:04"), true, ""},
		// 周一:边界与窗口行为。
		{"周一 13:00 CST (05:00 UTC,高峰前)", utc("2024-01-15 05:00").Format("2006-01-02 15:04"), true, ""},
		{"周一 14:00 CST (06:00 UTC,刚进高峰)", utc("2024-01-15 06:00").Format("2006-01-02 15:04"), false, ansiYellow},
		{"周一 15:00 CST (07:00 UTC,过半)", utc("2024-01-15 07:00").Format("2006-01-02 15:04"), false, ansiYellow},
		{"周一 17:31 CST (09:31 UTC,剩 29min)", utc("2024-01-15 09:31").Format("2006-01-02 15:04"), false, ansiRed},
		{"周一 18:00 CST (10:00 UTC,刚出高峰)", utc("2024-01-15 10:00").Format("2006-01-02 15:04"), true, ""},
		{"周一 19:00 CST (11:00 UTC,高峰后)", utc("2024-01-15 11:00").Format("2006-01-02 15:04"), true, ""},
		// 周二至周五 同样规则。
		{"周三 15:00 CST (07:00 UTC)", utc("2024-01-17 07:00").Format("2006-01-02 15:04"), false, ansiYellow},
		{"周五 15:00 CST (07:00 UTC)", utc("2024-01-19 07:00").Format("2006-01-02 15:04"), false, ansiYellow},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			tt, err := time.Parse("2006-01-02 15:04", c.utcTime)
			if err != nil {
				t.Fatal(err)
			}
			got := peakHourReminder(cfg, tt)
			if c.wantEmpty {
				if got != "" {
					t.Errorf("want empty, got %q", got)
				}
				return
			}
			if !strings.Contains(got, "高峰") {
				t.Errorf("want 含 '高峰', got %q", got)
			}
			if c.wantColor != "" && !strings.Contains(got, c.wantColor) {
				t.Errorf("want color %q, got %q", c.wantColor, got)
			}
		})
	}
}

// TestPeakHourReminderLayoutIntegration 验证三套排版都会拼接 peakHourReminder。
// 用真实的 renderStatusline 路径,固定 cfg.Statusline.Layout 驱动排版选择。
// 通过 ANTHROPIC_BASE_URL 指定测试会话。
func TestPeakHourReminderLayoutIntegration(t *testing.T) {
	isolateAppDirs(t, t.TempDir()) // 渲染会写 git 缓存,隔离掉真实缓存目录
	cfg := config.Default()
	t.Setenv("ANTHROPIC_BASE_URL", "https://open.bigmodel.cn/api/anthropic")

	// 周一 15:00 CST = 07:00 UTC(高峰内)。
	tt := time.Date(2024, 1, 15, 7, 0, 0, 0, time.UTC)
	in := &statuslineInput{}

	for _, layout := range []string{"a", "b", "c"} {
		t.Run("layout="+layout, func(t *testing.T) {
			cfg.Statusline.Layout = layout
			line := renderStatusline(in, cfg, tt)
			if !strings.Contains(line, "高峰") {
				t.Errorf("layout %s should embed 高峰 reminder, got %s", layout, line)
			}
		})
	}

	// 周末同一时间:三套排版都不应出现 "高峰"。
	cfg.Statusline.Layout = "a"
	ttWeekend := time.Date(2024, 1, 13, 7, 0, 0, 0, time.UTC)
	if line := renderStatusline(in, cfg, ttWeekend); strings.Contains(line, "高峰") {
		t.Errorf("weekend should not embed 高峰 reminder, got %s", line)
	}

	// 切到 MINIMAX 后:三套排版都不应出现 "高峰"(其他 provider 无特别要求)。
	t.Setenv("ANTHROPIC_BASE_URL", "https://api.minimax.io/anthropic")
	cfg.Statusline.Layout = "a"
	if line := renderStatusline(in, cfg, tt); strings.Contains(line, "高峰") {
		t.Errorf("non-Zhipu should not embed 高峰 reminder, got %s", line)
	}
}

// TestResolveStatuslineProviderRejectsLocal 守住用户报告的核心 bug:
// ANTHROPIC_BASE_URL=http://127.0.0.1 配 Claude 模型时,以前会被 modelProvider
// 兜底命中 KIMI(模型名含 "claude" 不命中,但缓存兜底会拿上次 Kimi 会话
// 的数据冒充当前会话)。现在 resolveStatuslineProvider 不再硬猜,直接
// 报错,statusline 也不显示配额段。
func TestResolveStatuslineProviderRejectsLocal(t *testing.T) {
	origBase := os.Getenv("ANTHROPIC_BASE_URL")
	defer func() {
		os.Setenv("ANTHROPIC_BASE_URL", origBase)
	}()
	os.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1")

	if _, err := resolveStatuslineProvider(); err == nil {
		t.Fatal("127.0.0.1 should not resolve to any provider (no rule matches)")
	}
}

// TestNoModelNameMatch 守住"不用模型名/ID 猜 provider"的契约 —
// modelProvider() 已删除,这里通过 statusline 全链路验证:即便 stdin 给
// 一个含 "kimi" 子串的模型名,只要 ANTHROPIC_BASE_URL 不指向 Kimi,
// 状态栏就不显示 Kimi 配额。
func TestNoModelNameMatch(t *testing.T) {
	origBase := os.Getenv("ANTHROPIC_BASE_URL")
	defer func() {
		os.Setenv("ANTHROPIC_BASE_URL", origBase)
	}()
	os.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1")

	cfg := config.Default()
	in := &statuslineInput{Model: struct {
		ID      string `json:"id"`
		Display string `json:"display_name"`
	}{ID: "kimi-k2-thinking", Display: "Kimi K2"}}
	// 渲染配额段 — 应当空(本地代理既非官方 Claude 也无法 resolve)。
	if got := quotaSegmentFor(in, cfg, time.Now()); got != "" {
		t.Errorf("local proxy should not show quota, got %q", got)
	}
}

// TestProviderBadgeDefaultOff 守住"provider badge 默认关"的契约:
// quotaSegmentForProvider 默认不输出 badge 文本 -- 多 provider 配置切换
// 时,看 quota 数字本身就够了。
func TestProviderBadgeDefaultOff(t *testing.T) {
	cfg := config.Default()
	rem := float64(72)
	snap := &model.Snapshot{
		Provider:  model.ProviderKimi,
		FetchedAt: time.Now(),
		ShortWindow: &model.Window{
			Label:        "5h",
			RemainingPct: &rem,
		},
	}
	// 默认关闭:输出里不该有 "KIMI"。
	if got := quotaSegmentForProvider(cfg, model.ProviderKimi, time.Now()); strings.Contains(got, "KIMI") {
		t.Errorf("default badge off — got %q should not contain KIMI", got)
	}
	if got := quotaSegmentForProvider(cfg, model.ProviderKimi, time.Now()); strings.Contains(got, ansiCyan) {
		t.Errorf("default badge off — got %q should not contain ansiCyan", got)
	}
	_ = snap // snap shape lock — 留作未来更细的断言入口
}

// TestQuotaSegmentForProviderMultiKey 多 key 场景:同 provider 两个 key
// 在 cache 里有 snapshot,渲染时两段并列,各自窗口可见。
//
// 用 cache.File 直接塞两条 entries 模拟 "watch 跑过后" 的状态,避免 mock HTTP。
func TestQuotaSegmentForProviderMultiKey(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{Provider: "kimi", Label: "alice", Token: "tk-a"},
		{Provider: "kimi", Label: "bob", Token: "tk-b"},
	}

	// 直接写 cache。
	dir := t.TempDir()
	isolateAppDirs(t, dir)
	cp, _ := cfg.CachePath()
	store := cache.New(cp)
	f := &cache.File{Entries: map[string]*cache.Entry{}}

	rem := 70.0
	reset := time.Now().Add(2 * time.Hour)
	makeSnap := func(label string, pct float64) *model.Snapshot {
		s := &model.Snapshot{
			Provider:  model.ProviderKimi,
			KeyLabel:  label,
			FetchedAt: time.Now(),
			ShortWindow: &model.Window{
				Label: "5h", RemainingPct: &pct, ResetAt: &reset,
			},
		}
		return s
	}
	f.SetLabeled(model.ProviderKimi, "alice", &cache.Entry{
		Snapshot: makeSnap("alice", rem), FetchedAt: time.Now(),
	})
	f.SetLabeled(model.ProviderKimi, "bob", &cache.Entry{
		Snapshot: makeSnap("bob", 90), FetchedAt: time.Now(),
	})
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	out := quotaSegmentForProvider(cfg, model.ProviderKimi, time.Now())

	// 多 key 段间用 " | " 分隔;每段前贴 label(magenta)。
	if !strings.Contains(out, "alice") {
		t.Errorf("multi-key output missing alice label: %s", out)
	}
	if !strings.Contains(out, "bob") {
		t.Errorf("multi-key output missing bob label: %s", out)
	}
	if !strings.Contains(out, ansiMagenta) {
		t.Errorf("label should be magenta: %s", out)
	}
	if !strings.Contains(out, "70%") || !strings.Contains(out, "90%") {
		t.Errorf("both percentages should appear: %s", out)
	}
	if !strings.Contains(out, "|") {
		t.Errorf("multi-key should have | separator: %s", out)
	}
}

// TestQuotaSegmentForProviderSingleKeyNoLabel 单 key 场景:不显示 label
// 与历史一致(单 key 老用户不应看到 "default" 字符串)。
func TestQuotaSegmentForProviderSingleKeyNoLabel(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{{Provider: "kimi", Label: "default", Token: "tk-only"}}

	dir := t.TempDir()
	isolateAppDirs(t, dir)
	cp, _ := cfg.CachePath()
	store := cache.New(cp)
	f := &cache.File{Entries: map[string]*cache.Entry{}}
	rem := 70.0
	reset := time.Now().Add(2 * time.Hour)
	f.Set(model.ProviderKimi, &cache.Entry{
		Snapshot: &model.Snapshot{
			Provider: model.ProviderKimi, FetchedAt: time.Now(),
			ShortWindow: &model.Window{Label: "5h", RemainingPct: &rem, ResetAt: &reset},
		},
		FetchedAt: time.Now(),
	})
	if err := store.Save(f); err != nil {
		t.Fatal(err)
	}

	out := quotaSegmentForProvider(cfg, model.ProviderKimi, time.Now())
	if strings.Contains(out, "default") {
		t.Errorf("single-key should NOT show 'default' label: %s", out)
	}
	if !strings.Contains(out, "70%") {
		t.Errorf("pct missing: %s", out)
	}
	if strings.Contains(out, "|") {
		t.Errorf("single-key should not have | separator: %s", out)
	}
}

func TestCtxUsageScalesToEffectiveWindow(t *testing.T) {
	for _, tc := range []struct {
		name, max, compact string
		want               float64
		limit              int64
	}{
		{"compact", "", "100000", 80, 100000},
		{"maximum", "100000", "", 80, 100000},
		{"smaller limit", "150000", "100000", 80, 100000},
		{"clamped", "", "50000", 100, 50000},
		{"larger compact window", "", "500000", 40, 200000},
	} {
		t.Run(tc.name, func(t *testing.T) {
			isolateAppDirs(t, t.TempDir())
			t.Setenv("CLAUDE_CODE_MAX_CONTEXT_TOKENS", tc.max)
			t.Setenv("CLAUDE_CODE_AUTO_COMPACT_WINDOW", tc.compact)
			total, pct := 200000.0, 40.0
			in := &statuslineInput{SessionID: "scaled"}
			in.ContextWindow = contextWindowInfo{ContextWindowSize: &total, UsedPercentage: &pct}
			cfg, now := config.Default(), time.Now()
			got, limit, ok := ctxUsage(in, cfg, now)
			if !ok || got != tc.want || limit != tc.limit {
				t.Fatalf("got (%v, %v, %v), want (%v, %v, true)", got, limit, ok, tc.want, tc.limit)
			}
			zero := 0.0
			in.ContextWindow.UsedPercentage = &zero
			got, _, ok = ctxUsage(in, cfg, now.Add(time.Second))
			if !ok || got != tc.want {
				t.Fatalf("transient zero: got %v, want %v", got, tc.want)
			}
		})
	}
}
