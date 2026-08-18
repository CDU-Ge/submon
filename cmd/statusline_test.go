package cmd

import (
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

func TestModelProviderDetection(t *testing.T) {
	cases := map[string]model.Provider{
		"glm-5.3":        model.ProviderZhipu,
		"GLM-4.7":        model.ProviderZhipu,
		"minimax-m2":     model.ProviderMinimax,
		"MiniMax-M2":     model.ProviderMinimax,
		"claude-fable-5": "",
	}
	for name, want := range cases {
		in := &statuslineInput{}
		in.Model.ID = name
		if got := in.modelProvider(); got != want {
			t.Errorf("modelProvider(%q) = %q, want %q", name, got, want)
		}
	}
}
