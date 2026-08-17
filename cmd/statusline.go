package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/spf13/cobra"
)

// statusline renders a robbyrussell-style line for Claude Code's statusLine
// setting: dir + git branch/dirty + model + context window, extended with
// subscription quotas. It is invoked on nearly every UI render, so it must:
//   - never touch the network for quotas (cache-only),
//   - always print one line fast, even when data is stale or missing,
//   - auto-pick the provider matching the session's ANTHROPIC_BASE_URL.
//
// Example output:
// ➜ submon git:(main)✗ GLM-5.3 [12.3k/200k 6%] │ ⚡ZHIPU 5h left 97%·4h22m │ W left 80%·5d13h (age 2m)
type contextWindowInfo struct {
	TotalInputTokens  *int64 `json:"total_input_tokens"`
	ContextWindowSize *int64 `json:"context_window_size"`
	UsedPercentage    *int64 `json:"used_percentage"`
}

type statuslineInput struct {
	Workspace struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	Model struct {
		ID      string `json:"id"`
		Display string `json:"display_name"`
	} `json:"model"`
	ContextWindow contextWindowInfo `json:"context_window"`
}

// modelProvider guesses which subscription the session's model belongs to:
// glm/zhipu names → zhipu, minimax names → minimax, else unknown.
func (in *statuslineInput) modelProvider() model.Provider {
	name := strings.ToLower(in.Model.ID + " " + in.Model.Display)
	switch {
	case strings.Contains(name, "glm"), strings.Contains(name, "zhipu"):
		return model.ProviderZhipu
	case strings.Contains(name, "minimax"):
		return model.ProviderMinimax
	}
	return ""
}

var statuslineProvider string

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "Claude Code 状态栏(目录/git/模型/上下文 + 配额)",
	Long: "输出 robbyrussell 风格的状态行:目录、git 分支、模型、上下文窗口用量\n" +
		"与订阅配额(只读缓存,不联网)。\n" +
		"provider 选择顺序: --provider > ANTHROPIC_BASE_URL 自动识别 > 配置默认值。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			cfg = config.Default() // statusline must never hard-fail
		}

		// Read Claude Code's JSON payload from stdin; absent fields degrade.
		var in statuslineInput
		raw, _ := io.ReadAll(os.Stdin)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &in)
		}

		out := cmd.OutOrStdout()
		fmt.Fprintln(out, renderStatusline(&in, cfg, time.Now()))
		return nil
	},
}

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[1;32m"
	ansiRed    = "\033[0;31m"
	ansiBRed   = "\033[1;31m"
	ansiCyan   = "\033[0;36m"
	ansiBlue   = "\033[1;34m"
	ansiYellow = "\033[0;33m"
	ansiDim    = "\033[2m"
)

func renderStatusline(in *statuslineInput, cfg *config.Config, now time.Time) string {
	// Model name: strip ANSI escapes and literal leftovers like "[1m]".
	modelName := stripANSI(in.Model.Display)

	cwd := in.Workspace.CurrentDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	dir := filepath.Base(cwd)

	var segs []string

	// ➜ dir git:(branch)✗
	prompt := ansiGreen + "➜" + ansiReset + " " + ansiCyan + dir + ansiReset
	if git := gitSegment(cwd); git != "" {
		prompt += " " + git
	}
	segs = append(segs, prompt)

	// model [ctx used/total pct%]
	if modelName != "" {
		seg := modelName
		if ctx := contextSegment(in.ContextWindow); ctx != "" {
			seg += " " + ctx
		}
		segs = append(segs, seg)
	} else if ctx := contextSegment(in.ContextWindow); ctx != "" {
		segs = append(segs, ctx)
	}

	// quotas + freshness
	if q := quotaSegment(cfg, in.modelProvider(), now); q != "" {
		segs = append(segs, q)
	}

	return strings.Join(segs, " ")
}

// gitSegment renders "git:(branch)✗" in robbyrussell colors; empty outside repos.
func gitSegment(cwd string) string {
	branch, err := gitBranch(cwd)
	if err != nil || branch == "" {
		return ""
	}
	dirty := ""
	if d, err := gitDirty(cwd); err == nil && d {
		dirty = " " + ansiYellow + "✗" + ansiReset
	}
	return ansiBlue + "git:(" + ansiReset + ansiBRed + branch + ansiReset + ansiBlue + ")" + ansiReset + dirty
}

func gitBranch(cwd string) (string, error) {
	out, err := exec.Command("git", "-C", cwd, "--no-optional-locks", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitDirty(cwd string) (bool, error) {
	err := exec.Command("git", "-C", cwd, "--no-optional-locks", "diff", "--quiet", "HEAD").Run()
	if err == nil {
		return false, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return true, nil // diff exited non-zero → dirty
	}
	return false, err
}

// contextSegment 渲染 "[12.3k/200k 6%]"。
//
// 两个 Claude Code 环境变量会修正"实际上下文上限":
//   - CLAUDE_CODE_MAX_CONTEXT_TOKENS: 覆盖 Claude Code 对当前模型上下文
//     窗口大小的假设(经网关路由、窗口与内置假设不符时使用)
//   - CLAUDE_CODE_AUTO_COMPACT_WINDOW: 直接决定 auto-compact 触发点
//     (100K–1M,例如 500000 = 对话到 500K 时压缩)
//
// 有效上限 = min(AUTO_COMPACT_WINDOW, 实际窗口, MAX_CONTEXT_TOKENS)。
// 设置了任一变量时显示 "实际窗口" 与有效上限的对比,否则与 Claude Code
// 自身报的百分比保持一致。
func contextSegment(cw contextWindowInfo) string {
	if cw.TotalInputTokens == nil || cw.ContextWindowSize == nil || *cw.ContextWindowSize <= 0 {
		return ""
	}
	usedK := fmt.Sprintf("%.1fk", float64(*cw.TotalInputTokens)/1000)
	total := *cw.ContextWindowSize

	effective := total
	notes := 0
	if v := envInt64("CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v > 0 && v < effective {
		effective = v
		notes++
	}
	if v := envInt64("CLAUDE_CODE_AUTO_COMPACT_WINDOW"); v > 0 && v < effective {
		effective = v
		notes++
	}

	// 以有效上限计算百分比(与 Claude Code 的压缩触发口径对齐)。
	pct := float64(*cw.TotalInputTokens) / float64(effective) * 100
	if notes > 0 {
		// 有环境变量干预:额外展示有效上限,百分比基于它。
		effK := fmt.Sprintf("%.0fk", float64(effective)/1000)
		return ansiDim + "[" + ansiReset + usedK + ansiDim + "·压缩点" + ansiReset + effK +
			fmt.Sprintf(" %.0f%%", pct) + ansiDim + "]" + ansiReset
	}
	if cw.UsedPercentage != nil {
		return ansiDim + "[" + ansiReset + usedK + "/" + totalK(total) + fmt.Sprintf(" %d%%", *cw.UsedPercentage) + ansiDim + "]" + ansiReset
	}
	return ansiDim + "[" + ansiReset + usedK + "/" + totalK(total) + fmt.Sprintf(" %.0f%%", pct) + ansiDim + "]" + ansiReset
}

func totalK(v int64) string { return fmt.Sprintf("%.0fk", float64(v)/1000) }

func envInt64(key string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// quotaSegment reads the cache and renders
// "⚡ZHIPU 5h 97% left·4h22m W 80% left·5d13h (age 2m)". The provider badge
// is dropped when the session's model already belongs to that provider
// (modelProvider matches) — the info would be redundant.
func quotaSegment(cfg *config.Config, modelProv model.Provider, now time.Time) string {
	name, err := resolveStatuslineProvider(cfg)
	if err != nil {
		return "" // quotas are an optional segment; never break the line
	}
	path, err := cfg.CachePath()
	if err != nil {
		return ""
	}
	file, err := cache.New(path).Load()
	if err != nil {
		return ""
	}
	entry := file.Get(name)
	if entry == nil || entry.Snapshot == nil {
		hint := "暂无数据,运行 submon status --force"
		if entry != nil && entry.LastErr != "" {
			hint = entry.LastErr
		}
		return ansiDim + "⚡" + badgeText(name) + " " + hint + ansiReset
	}
	s := entry.Snapshot

	var parts []string
	if s.ShortWindow != nil {
		parts = append(parts, colorizeWindow("5h", s.ShortWindow, now, cfg))
	}
	if s.LongWindow != nil {
		parts = append(parts, colorizeWindow("W", s.LongWindow, now, cfg))
	}
	if len(parts) == 0 {
		return ""
	}
	out := ""
	if modelProv != name {
		out = ansiDim + "⚡" + ansiReset + providerBadge(s.Provider) + " "
	}
	// Window separator: thin dim slash groups 5h vs weekly while staying quiet.
	out += strings.Join(parts, ansiDim+"/"+ansiReset)

	// Freshness: age once stale, or countdown to next refresh while fresh.
	age := now.Sub(s.FetchedAt)
	ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second
	switch {
	case age > ttl:
		out += ansiYellow + fmt.Sprintf(" (age %s)", humanDuration(age)) + ansiReset
	default:
		next := ttl - age
		out += ansiDim + fmt.Sprintf(" (↻%s)", humanDuration(next)) + ansiReset
	}
	return out
}

// badgeText is the plain (uncolored) provider label.
func badgeText(p model.Provider) string { return strings.ToUpper(string(p)) }

// providerBadge renders the provider tag, uppercase for distinction.
func providerBadge(p model.Provider) string {
	label := strings.ToUpper(string(p))
	switch p {
	case model.ProviderZhipu:
		return ansiCyan + label + ansiReset
	case model.ProviderMinimax:
		return ansiYellow + label + ansiReset
	}
	return label
}

// resolveStatuslineProvider: --provider flag > ANTHROPIC_BASE_URL > config.
func resolveStatuslineProvider(cfg *config.Config) (model.Provider, error) {
	if statuslineProvider != "" {
		name := model.Provider(statuslineProvider)
		if name != model.ProviderZhipu && name != model.ProviderMinimax {
			return "", fmt.Errorf("unknown provider %q", statuslineProvider)
		}
		return name, nil
	}
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		if name, err := provider.DetectFromBaseURL(base); err == nil {
			return name, nil
		}
	}
	if cfg.Statusline.DefaultProvider != "" {
		name := model.Provider(cfg.Statusline.DefaultProvider)
		if name == model.ProviderZhipu || name == model.ProviderMinimax {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot resolve provider")
}

// colorizeWindow renders "5h 97% left·4h22m" (pct, then left/used marker,
// then reset countdown).
func colorizeWindow(prefix string, w *model.Window, now time.Time, cfg *config.Config) string {
	if w.RemainingPct == nil && w.UsedPct == nil {
		return prefix + " n/a"
	}
	pctStr, color := "n/a", ansiGreen
	if w.RemainingPct != nil {
		pct := *w.RemainingPct
		pctStr = fmt.Sprintf("%.0f%%", pct)
		color = pctColor(pct, cfg)
	}
	out := prefix + " " + color + pctStr + ansiReset + " " + ansiDim + "left" + ansiReset
	if w.ResetAt != nil {
		out += ansiDim + "·" + humanDuration(time.Until(*w.ResetAt)) + ansiReset
	}
	return out
}

// stripANSI removes escape sequences and their literal residues (e.g. the
// "[1m]" in "GLM-5.2[1m]").
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\033':
			inEsc = true
		case inEsc && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
			inEsc = false // end of CSI sequence
		case !inEsc:
			b.WriteByte(c)
		}
	}
	out := b.String()
	// Literal residues like "[1m]" / "[0m]" (escape already stripped elsewhere).
	out = strings.ReplaceAll(out, "[1m]", "")
	out = strings.ReplaceAll(out, "[0m]", "")
	return strings.TrimSpace(out)
}

func init() {
	statuslineCmd.Flags().StringVar(&statuslineProvider, "provider", "", "要展示的 provider(zhipu|minimax)")
	rootCmd.AddCommand(statuslineCmd)
}
