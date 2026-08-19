package cmd

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

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
	in.Workspace.CurrentDir = "/home/ge/Projects/subscription-monitor"
	in.Model.Display = "GLM-5.3[1m]"
	in.Effort.Level = "xhigh"
	in.FastMode = true
	used, total := int64(12345), int64(200000)
	pctx := int64(6)
	in.ContextWindow = contextWindowInfo{TotalInputTokens: &used, ContextWindowSize: &total, UsedPercentage: &pctx}

	// Quota segment reads the real cache path; absent data degrades to a
	// hint, which still proves the rest of the line renders.
	line := renderStatusline(in, cfg, time.Now())
	for _, want := range []string{"subscription-monitor", "main", "GLM-5.3", "200k", "[", "]", "6%", "xhigh", "fast"} {
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

	// git segment presence depends on cwd being a repo; the test repo is one.
	if !strings.Contains(line, "main") {
		t.Errorf("expected bare branch name in repo: %s", line)
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
	branch, err := gitBranch(".")
	if err != nil {
		t.Skipf("not in a git repo: %v", err)
	}
	gotBranch, _ := gitInlineSegment(".")
	if gotBranch != branch {
		t.Errorf("gitInlineSegment() branch = %q, want %q", gotBranch, branch)
	}
	// 不应有 ANSI 包装,纯文本。
	if strings.Contains(gotBranch, "\033") {
		t.Errorf("gitInlineSegment should return plain text, got %q", gotBranch)
	}
}

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

	badge := providerBadge(model.ProviderMinimax)
	if !strings.Contains(badge, "MINIMAX") {
		t.Errorf("badge should be uppercase provider, got %s", badge)
	}
}

func TestPeakHourReminder(t *testing.T) {
	cfg := config.Default()

	// bind/unbind the package-level --provider flag so resolveStatuslineProvider
	// 走我们想要的分支,而不是测试环境里的 ANTHROPIC_BASE_URL/config 兜底。
	orig := statuslineProvider
	defer func() { statuslineProvider = orig }()

	// 1. 非 Zhipu:空(其他 provider 不打提醒,这是契约)。
	statuslineProvider = "minimax"
	if got := peakHourReminder(cfg, time.Now()); got != "" {
		t.Errorf("non-Zhipu should be empty, got %q", got)
	}
	statuslineProvider = "opencode"
	if got := peakHourReminder(cfg, time.Now()); got != "" {
		t.Errorf("non-Zhipu should be empty, got %q", got)
	}

	// 后续用例全部用 Zhipu。
	statuslineProvider = "zhipu"

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
// 走 statuslineProvider="zhipu" 路径,避免被测试环境的 ANTHROPIC_BASE_URL
// 干扰(那个会指向 MINIMAX,真实缓存里也只有 MINIMAX 数据)。
func TestPeakHourReminderLayoutIntegration(t *testing.T) {
	cfg := config.Default()
	orig := statuslineProvider
	defer func() { statuslineProvider = orig }()
	statuslineProvider = "zhipu"

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
	statuslineProvider = "minimax"
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
	origProv := statuslineProvider
	origBase := os.Getenv("ANTHROPIC_BASE_URL")
	defer func() {
		statuslineProvider = origProv
		os.Setenv("ANTHROPIC_BASE_URL", origBase)
	}()
	statuslineProvider = "" // 清掉 --provider,只走 ANTHROPIC_BASE_URL 分支。
	os.Setenv("ANTHROPIC_BASE_URL", "http://127.0.0.1")

	cfg := config.Default()
	if _, err := resolveStatuslineProvider(cfg); err == nil {
		t.Fatal("127.0.0.1 should not resolve to any provider (no rule matches)")
	}
}

// TestNoModelNameMatch 守住"不用模型名/ID 猜 provider"的契约 —
// modelProvider() 已删除,这里通过 statusline 全链路验证:即便 stdin 给
// 一个含 "kimi" 子串的模型名,只要 ANTHROPIC_BASE_URL 不指向 Kimi,
// 状态栏就不显示 Kimi 配额。
func TestNoModelNameMatch(t *testing.T) {
	origProv := statuslineProvider
	origBase := os.Getenv("ANTHROPIC_BASE_URL")
	defer func() {
		statuslineProvider = origProv
		os.Setenv("ANTHROPIC_BASE_URL", origBase)
	}()
	statuslineProvider = ""
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
// 时,看 quota 数字本身就够了。需要排查时用 --show-provider 或
// SUBMON_SHOW_PROVIDER=1 临时打开。
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
