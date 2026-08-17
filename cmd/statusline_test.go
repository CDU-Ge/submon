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
		"GLM-5.3":         "GLM-5.3",
		"GLM-5.2[1m]":     "GLM-5.2",
		"\033[1mHi\033[0m": "Hi",
		"":                "",
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
	used, total := int64(12345), int64(200000)
	pctx := int64(6)
	in.ContextWindow = contextWindowInfo{TotalInputTokens: &used, ContextWindowSize: &total, UsedPercentage: &pctx}

	// Quota segment reads the real cache path; absent data degrades to a
	// hint, which still proves the rest of the line renders.
	line := renderStatusline(in, cfg, time.Now())
	for _, want := range []string{"➜", "subscription-monitor", "GLM-5.3", "12.3k/200k", "6%"} {
		if !strings.Contains(line, want) {
			t.Errorf("line missing %q: %s", want, line)
		}
	}
	if strings.Contains(line, "[1m") {
		t.Errorf("model name ANSI residue leaked: %s", line)
	}

	// git segment presence depends on cwd being a repo; the test repo is one.
	if !strings.Contains(line, "git:(") {
		t.Errorf("expected git segment in repo: %s", line)
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
	// Order: "<prefix> <pct>% left·<countdown>" — pct before the left marker.
	if i, j := strings.Index(out, "3%"), strings.Index(out, "left"); i == -1 || j == -1 || i > j {
		t.Errorf("expected 'pct%% left' order, got %s", out)
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
		"glm-5.3":          model.ProviderZhipu,
		"GLM-4.7":          model.ProviderZhipu,
		"minimax-m2":       model.ProviderMinimax,
		"MiniMax-M2":       model.ProviderMinimax,
		"claude-fable-5":   "",
	}
	for name, want := range cases {
		in := &statuslineInput{}
		in.Model.ID = name
		if got := in.modelProvider(); got != want {
			t.Errorf("modelProvider(%q) = %q, want %q", name, got, want)
		}
	}
}
