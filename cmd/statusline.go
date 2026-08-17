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
	ansiReset   = "\033[0m"
	ansiGreen   = "\033[1;32m"
	ansiRed     = "\033[0;31m"
	ansiBRed    = "\033[1;31m"
	ansiCyan    = "\033[0;36m"
	ansiBlue    = "\033[1;34m"
	ansiYellow  = "\033[0;33m"
	ansiMagenta = "\033[0;35m"
	ansiDim     = "\033[2m"
)

// contextPlainSegment: layout B 的上下文段 "ctx 15% used"。
func contextPlainSegment(cw contextWindowInfo) string {
	if cw.TotalInputTokens == nil || cw.ContextWindowSize == nil || *cw.ContextWindowSize <= 0 {
		return ""
	}
	effective := *cw.ContextWindowSize
	if v := envInt64("CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v > 0 && v < effective {
		effective = v
	}
	if v := envInt64("CLAUDE_CODE_AUTO_COMPACT_WINDOW"); v > 0 && v < effective {
		effective = v
	}
	pct := float64(*cw.TotalInputTokens) / float64(effective) * 100
	return fmt.Sprintf("ctx %s%.0f%%%s used", ansiGreen, pct, ansiReset)
}

// quotaDetailedSegment: layout B 的配额段 "5h 72% left (1h9m) · weekly 74% left (5d10h)"。
func quotaDetailedSegment(cfg *config.Config, modelProv model.Provider, now time.Time) string {
	segs := quotaWindowSegs(cfg, modelProv, now, true)
	return strings.Join(segs, ansiDim+" · "+ansiReset)
}

// quotaMinimalSegment: layout C 的配额段 "72%/74% (↻2m)"。
func quotaMinimalSegment(cfg *config.Config, modelProv model.Provider, now time.Time) string {
	segs := quotaWindowSegs(cfg, modelProv, now, false)
	if len(segs) == 0 {
		return ""
	}
	out := strings.Join(segs, ansiDim+"/"+ansiReset)
	if age := freshTrailer(cfg, now); age != "" {
		out += " " + age
	}
	return out
}

// quotaWindowSegs 抽出各窗口渲染:verbose=true 带标签与倒计时,极简只留 %。
func quotaWindowSegs(cfg *config.Config, modelProv model.Provider, now time.Time, verbose bool) []string {
	name, err := resolveStatuslineProvider(cfg)
	if err != nil {
		return nil
	}
	path, err := cfg.CachePath()
	if err != nil {
		return nil
	}
	file, err := cache.New(path).Load()
	if err != nil {
		return nil
	}
	entry := file.Get(name)
	if entry == nil || entry.Snapshot == nil {
		maybeSelfHeal(path, entry, cfg, now)
		return []string{ansiDim + "⚡" + badgeText(name) + " 刷新中…" + ansiReset}
	}
	s := entry.Snapshot
	if now.Sub(s.FetchedAt) > time.Duration(cfg.Cache.TTLSeconds)*time.Second {
		maybeSelfHeal(path, entry, cfg, now)
	}

	var segs []string
	for _, w := range []*model.Window{s.ShortWindow, s.LongWindow} {
		if w == nil || w.RemainingPct == nil {
			continue
		}
		label := ""
		switch w.Label {
		case "5h":
			label = "5h"
		case "week":
			label = "weekly"
		default:
			label = w.Label
		}
		color := pctColor(*w.RemainingPct, cfg)
		if !verbose {
			segs = append(segs, color+fmt.Sprintf("%.0f%%", *w.RemainingPct)+ansiReset)
			continue
		}
		seg := fmt.Sprintf("%s %s%.0f%%%s left", ansiDim+label+ansiReset, color, *w.RemainingPct, ansiReset)
		if w.ResetAt != nil {
			seg += fmt.Sprintf(" %s(%s)%s", ansiDim, humanDuration(time.Until(*w.ResetAt)), ansiReset)
		}
		segs = append(segs, seg)
	}
	if len(segs) == 0 {
		return nil
	}
	if verbose && modelProv != name {
		segs = append([]string{ansiDim + "⚡" + ansiReset + providerBadge(s.Provider)}, segs...)
	}
	return segs
}

// freshTrailer: 新鲜度尾标(极简方案用)。
func freshTrailer(cfg *config.Config, now time.Time) string {
	name, err := resolveStatuslineProvider(cfg)
	if err != nil {
		return ""
	}
	path, err := cfg.CachePath()
	if err != nil {
		return ""
	}
	file, err := cache.New(path).Load()
	if err != nil {
		return ""
	}
	e := file.Get(name)
	if e == nil || e.Snapshot == nil {
		return ""
	}
	age := now.Sub(e.Snapshot.FetchedAt)
	ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second
	if age > ttl {
		return ansiYellow + fmt.Sprintf("(刷新中·%s前)", humanDuration(age)) + ansiReset
	}
	return ansiDim + fmt.Sprintf("(↻%s)", humanDuration(ttl-age)) + ansiReset
}

// renderStatusline 按配置的 layout 方案渲染。
//
//	layout a(默认,当前方案): ➜ dir git:(main)✗ Model [ctx] 5h 72% left·1h9m weekly 74% left (↻2m)
//	layout b(信息优先):      Model · ctx 15% used · dir · ⎇main · 5h 72% left (1h9m) · weekly 74% left (5d10h)
//	layout c(极简):         dir main · 15% · 72%/74% (↻2m)
func renderStatusline(in *statuslineInput, cfg *config.Config, now time.Time) string {
	modelName := stripANSI(in.Model.Display)

	cwd := in.Workspace.CurrentDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	dir := filepath.Base(cwd)

	switch cfg.Statusline.Layout {
	case "b":
		return renderLayoutB(in, cfg, modelName, dir, cwd, now)
	case "c":
		return renderLayoutC(in, cfg, modelName, dir, cwd, now)
	default:
		return renderLayoutA(in, cfg, modelName, dir, cwd, now)
	}
}

// renderLayoutA: robbyrussell 经典布局(当前默认)。
func renderLayoutA(in *statuslineInput, cfg *config.Config, modelName, dir, cwd string, now time.Time) string {
	var segs []string

	prompt := ansiGreen + "➜" + ansiReset + " " + ansiCyan + dir + ansiReset
	if git := gitSegment(cwd); git != "" {
		prompt += " " + git
	}
	segs = append(segs, prompt)

	if modelName != "" {
		seg := modelName
		if ctx := contextSegment(in.ContextWindow); ctx != "" {
			seg += " " + ctx
		}
		segs = append(segs, seg)
	} else if ctx := contextSegment(in.ContextWindow); ctx != "" {
		segs = append(segs, ctx)
	}

	if q := quotaSegment(cfg, in.modelProvider(), now); q != "" {
		segs = append(segs, q)
	}
	return strings.Join(segs, " ")
}

// renderLayoutB: 信息优先 —— model 打头,细粒度分段,branch 用 ⎇ 符号。
// Model · ctx 15% used · dir · ⎇main · 5h 72% left (1h9m) · weekly 74% left (5d10h)
func renderLayoutB(in *statuslineInput, cfg *config.Config, modelName, dir, cwd string, now time.Time) string {
	var segs []string

	if modelName != "" {
		segs = append(segs, ansiCyan+modelName+ansiReset)
	}
	if ctx := contextPlainSegment(in.ContextWindow); ctx != "" {
		segs = append(segs, ctx)
	}
	segs = append(segs, ansiYellow+dir+ansiReset)
	if branch, err := gitBranch(cwd); err == nil && branch != "" {
		dirtyMark := ""
		if d, err := gitDirty(cwd); err == nil && d {
			dirtyMark = ansiRed + "*" + ansiReset
		}
		segs = append(segs, ansiDim+"⎇"+ansiReset+ansiMagenta+branch+dirtyMark+ansiReset)
	}
	if q := quotaDetailedSegment(cfg, in.modelProvider(), now); q != "" {
		segs = append(segs, q)
	}
	sep := ansiDim + " · " + ansiReset
	return strings.Join(segs, sep)
}

// renderLayoutC: 极简 —— 只留 dir、branch、上下文%、两个配额%。
// dir main · 15% · 72%/74% (↻2m)
func renderLayoutC(in *statuslineInput, cfg *config.Config, modelName, dir, cwd string, now time.Time) string {
	var segs []string
	segs = append(segs, ansiCyan+dir+ansiReset)
	if branch, err := gitBranch(cwd); err == nil && branch != "" {
		segs = append(segs, ansiDim+branch+ansiReset)
	}
	if in.ContextWindow.UsedPercentage != nil {
		segs = append(segs, fmt.Sprintf("%s%d%%%s", ansiGreen, *in.ContextWindow.UsedPercentage, ansiReset))
	} else if in.ContextWindow.TotalInputTokens != nil && in.ContextWindow.ContextWindowSize != nil && *in.ContextWindow.ContextWindowSize > 0 {
		pct := float64(*in.ContextWindow.TotalInputTokens) / float64(*in.ContextWindow.ContextWindowSize) * 100
		segs = append(segs, fmt.Sprintf("%s%.0f%%%s", ansiGreen, pct, ansiReset))
	}
	if q := quotaMinimalSegment(cfg, in.modelProvider(), now); q != "" {
		segs = append(segs, q)
	}
	sep := ansiDim + " · " + ansiReset
	return strings.Join(segs, sep)
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
	used := humanTokens(float64(*cw.TotalInputTokens))
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
		return ansiDim + "[" + ansiReset + used + ansiDim + "·压缩点" + ansiReset +
			humanTokens(float64(effective)) + fmt.Sprintf(" %.0f%%", pct) + ansiDim + "]" + ansiReset
	}
	pctStr := fmt.Sprintf(" %.0f%%", pct)
	if cw.UsedPercentage != nil {
		pctStr = fmt.Sprintf(" %d%%", *cw.UsedPercentage)
	}
	return ansiDim + "[" + ansiReset + used + "/" + humanTokens(float64(total)) + pctStr + ansiDim + "]" + ansiReset
}

// humanTokens 把 token 数格式化为紧凑形式:1.2M / 183.4k / 890。
func humanTokens(v float64) string {
	switch {
	case v >= 1_000_000:
		return trimZero(fmt.Sprintf("%.1fM", v/1_000_000))
	case v >= 10_000:
		// 10k 以上保留一位小数:183.4k
		return trimZero(fmt.Sprintf("%.1fk", v/1_000))
	case v >= 1_000:
		return fmt.Sprintf("%.0fk", v/1_000)
	default:
		return fmt.Sprintf("%.0f", v)
	}
}

// trimZero 去掉 "1.0M" 的 ".0"。
func trimZero(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, ".0M", "M"), ".0k", "k")
}

func envInt64(key string) int64 {
	v, err := strconv.ParseInt(strings.TrimSpace(os.Getenv(key)), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// quotaSegment 读缓存渲染配额段。provider badge 在"当前模型属于该
// provider"时省略(信息冗余)。
//
// 缓存过期时触发自愈:fork 一个后台静默 watch 刷新(不阻塞本次渲染),
// 受 RefreshedAt 节流,失败也不会打爆 API。
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
		maybeSelfHeal(path, entry, cfg, now)
		hint := "暂无数据,刷新中…"
		if entry != nil && entry.LastErr != "" {
			hint = entry.LastErr
		}
		return ansiDim + "⚡" + badgeText(name) + " " + hint + ansiReset
	}
	s := entry.Snapshot

	ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second
	if now.Sub(s.FetchedAt) > ttl {
		maybeSelfHeal(path, entry, cfg, now)
	}

	var parts []string
	if s.ShortWindow != nil {
		parts = append(parts, colorizeWindow("5h", s.ShortWindow, now, cfg))
	}
	if s.LongWindow != nil {
		parts = append(parts, colorizeWindow("weekly", s.LongWindow, now, cfg))
	}
	if len(parts) == 0 {
		return ""
	}
	out := ""
	if modelProv != name {
		out = ansiDim + "⚡" + ansiReset + providerBadge(s.Provider) + " "
	}
	// 窗口间分隔:dim " - ",比 "·" 更有分组感。
	out += strings.Join(parts, ansiDim+" - "+ansiReset)

	// 新鲜度:过期显示黄色 age,新鲜显示距下次刷新倒计时。
	age := now.Sub(s.FetchedAt)
	switch {
	case age > ttl:
		out += ansiYellow + fmt.Sprintf(" (刷新中·%s前)", humanDuration(age)) + ansiReset
	default:
		next := ttl - age
		out += ansiDim + fmt.Sprintf(" (↻%s)", humanDuration(next)) + ansiReset
	}
	return out
}

// maybeSelfHeal 在缓存过期时 fork 后台 watch 刷新。节流:距上次尝试
// 不足 TTL/2 时跳过,避免 API 故障时每次渲染都发起请求。
func maybeSelfHeal(cachePath string, entry *cache.Entry, cfg *config.Config, now time.Time) {
	half := time.Duration(cfg.Cache.TTLSeconds) * time.Second / 2
	if entry != nil && !entry.RefreshedAt.IsZero() && now.Sub(entry.RefreshedAt) < half {
		return
	}
	// 先落 RefreshedAt(尽力而为),再 fork 静默刷新进程。
	if entry != nil {
		entry.RefreshedAt = now
		if f, err := cache.New(cachePath).Load(); err == nil {
			if e := f.Get(entry.Snapshot.Provider); e != nil {
				e.RefreshedAt = now
				_ = cache.New(cachePath).Save(f)
			}
		}
	}
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe, "watch", "--quiet")
	cmd.Stdout = io.Discard
	cmd.Stderr = io.Discard
	_ = cmd.Start()
	// 父进程(statusline)立即退出,孤儿进程由 init 收养,刷新完写缓存后退出。
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

// colorizeWindow 渲染一个窗口段,两种形态:
//
//	short(5h):      "3h8m 86% left"   —— 倒计时打头(倒计时是主信息)
//	long(weekly):   "weekly 70% left 5d7h" —— 标签打头,倒计时收尾
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
	left := ansiDim + "left" + ansiReset

	// 5h 窗口:倒计时在前(时间紧迫感),百分比在后。
	if w.Label == "5h" && w.ResetAt != nil {
		return ansiDim + humanDuration(time.Until(*w.ResetAt)) + ansiReset + " " +
			color + pctStr + ansiReset + " " + left
	}
	// weekly/其他:标签 + 百分比 + 倒计时收尾。
	out := ansiDim + prefix + ansiReset + " " + color + pctStr + ansiReset + " " + left
	if w.ResetAt != nil {
		out += " " + ansiDim + humanDuration(time.Until(*w.ResetAt)) + ansiReset
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
