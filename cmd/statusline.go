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

// statusline renders a single-line status bar for Claude Code's statusLine
// setting, extended with subscription quotas. It is invoked on nearly every UI
// render, so it must:
//   - never touch the network for quotas (cache-only),
//   - always print one line fast, even when data is stale or missing,
//   - auto-pick the provider matching the session's ANTHROPIC_BASE_URL.
//
// Design (layout a, default):
//
//	submon ⎇main  GLM-5.3  ctx 12.3k/200k 6%  xhigh  fast  5h 4h50m 90%  weekly 53% 4d13h  ↻3m
//
// Layout principle: left = stable (dir, branch, model), center = the thing the
// user watches every frame (context tokens), right = volatile (effort, fast,
// rate-limit windows). Symbols are kept to a minimum: only ⎇ (git convention)
// and * (dirty marker) survive.
type contextWindowInfo struct {
	TotalInputTokens  *int64 `json:"total_input_tokens"`
	ContextWindowSize *int64 `json:"context_window_size"`
	UsedPercentage    *int64 `json:"used_percentage"`
}

// rateLimitWindow 是 Claude Code stdin 里的官方订阅限额窗口。
type rateLimitWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"` // epoch 秒
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
	RateLimits    struct {
		FiveHour rateLimitWindow `json:"five_hour"`
		SevenDay rateLimitWindow `json:"seven_day"`
	} `json:"rate_limits"`
	FastMode bool `json:"fast_mode"`
	Effort   struct {
		Level string `json:"level"`
	} `json:"effort"`
}

// 颜色序列 —— 每次 useColor() 变化(尤其是 --no-color flag)由
// refreshColorState() 重算;const 不行,因为要响应 PersistentPreRun 后
// 才被 cobra 解析的 flag。代码常量本身(\033[<code>m)是定死的。
const (
	colorReset   = "0"
	colorGreen   = "1;32"
	colorRed     = "0;31"
	colorBRed    = "1;31"
	colorCyan    = "0;36"
	colorBlue    = "1;34"
	colorYellow  = "0;33"
	colorMagenta = "0;35"
	colorDim     = "2"
	colorWhite   = "0;39"
)

// ANSI 转义序列变量 —— useColor() 为 false 时为空串,避免污染管道/CI 日志。
// 由 refreshColorState() 在 rootCmd PersistentPreRun 时按当前 env/flag/TTY
// 状态刷新一次(测试代码也可直接赋值强制开启/关闭)。
var (
	ansiReset   = "\033[0m"
	ansiGreen   = "\033[1;32m"
	ansiRed     = "\033[0;31m"
	ansiBRed    = "\033[1;31m"
	ansiCyan    = "\033[0;36m"
	ansiBlue    = "\033[1;34m"
	ansiYellow  = "\033[0;33m"
	ansiMagenta = "\033[0;35m"
	ansiDim     = "\033[2m"
	// 辅助信息用默认前景色(终端主题的正常文字色),保证可读性;
	// dim(灰)在浅色/低对比主题下难以辨认,仅保留给极不重要的细节。
	ansiWhite = "\033[0;39m"
)

// refreshColorState 按 useColor() 重新计算所有 ansiXxx 变量。RootCmd
// PersistentPreRun 调用一次 — 此时 cobra 已解析完 argv, --no-color flag
// 已确定;env 变量自进程启动不变,只算一次足够。
func refreshColorState() {
	if useColor() {
		ansiReset = "\033[" + colorReset + "m"
		ansiGreen = "\033[" + colorGreen + "m"
		ansiRed = "\033[" + colorRed + "m"
		ansiBRed = "\033[" + colorBRed + "m"
		ansiCyan = "\033[" + colorCyan + "m"
		ansiBlue = "\033[" + colorBlue + "m"
		ansiYellow = "\033[" + colorYellow + "m"
		ansiMagenta = "\033[" + colorMagenta + "m"
		ansiDim = "\033[" + colorDim + "m"
		ansiWhite = "\033[" + colorWhite + "m"
	} else {
		ansiReset = ""
		ansiGreen = ""
		ansiRed = ""
		ansiBRed = ""
		ansiCyan = ""
		ansiBlue = ""
		ansiYellow = ""
		ansiMagenta = ""
		ansiDim = ""
		ansiWhite = ""
	}
}

var statuslineProvider string
var statuslineShowProvider bool

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "渲染 Claude Code 状态栏",
	Long: "输出单行状态栏:左稳定(目录/git 分支/模型)→ 中关心(token 使用)\n" +
		"→ 右变动(effort/fast/订阅配额)。只读缓存,不联网。\n" +
		"provider 选择顺序: --provider > $ANTHROPIC_BASE_URL 自动识别(内建规则 + [[provider_detection]] 用户规则)。\n" +
		"都没有命中 → statusline 不显示配额段,不强行猜 provider。",
	Example: "  # 在 Claude Code settings.json 里:\n" +
		"  submon install              # 一键接入 ~/.claude/settings.json\n\n" +
		"  # 手动调用(不通过 CC):\n" +
		"  echo '{\"model\":{\"display_name\":\"GLM-5.3\"}}' | submon statusline\n" +
		"  submon statusline --provider zhipu --show-provider",
	RunE: func(cmd *cobra.Command, args []string) error {
		// Claude Code 经管道捕获 statusline 输出并渲染 ANSI — 非 TTY
		// 不作为关色依据。PersistentPreRun 已按 TTY 规则算过一次,
		// 这里翻开关后重算(纯内存运算,秒回预算内)。
		allowPipeColor = true
		refreshColorState()

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

// isOfficialClaude 报告当前会话是否跑在 Claude 官方订阅上(而非网关)。
// 仅此时 stdin 的 rate_limits 字段才存在,也仅此时启用 claude 伪 provider。
func isOfficialClaude() bool {
	base := strings.ToLower(os.Getenv("ANTHROPIC_BASE_URL"))
	return base == "" || strings.Contains(base, "anthropic.com")
}

// contextPlainSegment: layout B 的上下文段 "ctx 已用 15%"。
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
	return fmt.Sprintf("ctx %s已用 %.0f%%%s", ansiGreen, pct, ansiReset)
}

// quotaDetailedSegment: layout B 的配额段 "5h 剩 72%（1h9m 后重置） · weekly 剩 74%（5d10h 后重置）"。
func quotaDetailedSegment(cfg *config.Config, now time.Time) string {
	segs := quotaWindowSegs(cfg, now, true)
	return strings.Join(segs, ansiWhite+" · "+ansiReset)
}

// quotaMinimalSegment: layout C 的配额段 "72%/74% (↻2m)"。
func quotaMinimalSegment(cfg *config.Config, now time.Time) string {
	segs := quotaWindowSegs(cfg, now, false)
	if len(segs) == 0 {
		return ""
	}
	out := strings.Join(segs, ansiWhite+"/"+ansiReset)
	if age := freshTrailer(cfg, now); age != "" {
		out += " " + age
	}
	return out
}

// quotaWindowSegs 抽出各窗口渲染:verbose=true 带标签与倒计时,极简只留 %。
// 多 key 场景:每个 Key 独立渲染,段间用 " | " 分隔 — 与 layout A 对齐。
func quotaWindowSegs(cfg *config.Config, now time.Time, verbose bool) []string {
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

	keys := cfg.ProviderKeys(name)
	if len(keys) == 0 {
		keys = []config.Account{{Label: "default"}}
	}

	var perKey [][]string
	for _, k := range keys {
		entry := file.GetLabeled(name, k.Label)
		if entry == nil || entry.Snapshot == nil {
			maybeSelfHeal(path, entry, cfg, now)
			continue
		}
		if now.Sub(entry.Snapshot.FetchedAt) > time.Duration(cfg.Cache.TTLSeconds)*time.Second {
			maybeSelfHeal(path, entry, cfg, now)
		}
		var segs []string
		for _, w := range []*model.Window{entry.Snapshot.ShortWindow, entry.Snapshot.LongWindow} {
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
			seg := fmt.Sprintf("%s %s剩 %.0f%%%s", ansiWhite+label+ansiReset, color, *w.RemainingPct, ansiReset)
			if w.ResetAt != nil {
				seg += fmt.Sprintf(" %s(%s 后重置)%s", ansiWhite, humanDuration(time.Until(*w.ResetAt)), ansiReset)
			}
			segs = append(segs, seg)
		}
		if len(segs) > 0 {
			// 多 key 时把 label 贴在段首,单 key 与历史一致。
			if k.Label != "" && k.Label != "default" {
				segs = append([]string{ansiMagenta + k.Label + ansiReset}, segs...)
			}
			perKey = append(perKey, segs)
		}
	}
	if len(perKey) == 0 {
		return nil
	}
	out := make([]string, 0, len(perKey))
	for _, s := range perKey {
		out = append(out, strings.Join(s, ansiWhite+" - "+ansiReset))
	}
	return out
}

// peakHourReminder Zhipu 高峰期提醒。
//
// 高峰期:周一至周五 14:00-18:00 (UTC+8,Asia/Shanghai 标准时)。
// 仅在窗口内显示,窗口外或非 Zhipu provider 返回空——
// 状态栏每帧渲染,常驻信息会让真正紧急的提醒失去注意力。
//
// 颜色:剩余 < 30min 转红(收尾期),其他黄色(高峰期进行中);
// 标签 "高峰" 用白色(辅助信息,不动色,沿用 ansiWhite 约定)。
func peakHourReminder(cfg *config.Config, now time.Time) string {
	name, err := resolveStatuslineProvider(cfg)
	if err != nil || name != model.ProviderZhipu {
		return ""
	}
	const cstOff = 8 * 3600
	cst := time.FixedZone("CST", cstOff)
	local := now.In(cst)
	wd := local.Weekday()
	if wd < time.Monday || wd > time.Friday {
		return ""
	}
	hour := local.Hour()
	if hour < 14 || hour >= 18 {
		return ""
	}
	endAt := time.Date(local.Year(), local.Month(), local.Day(), 18, 0, 0, 0, cst)
	// 关键:用入参 now 算差,不是 time.Until()(后者读真实时钟,测试与自愈节流都错位)。
	remaining := endAt.Sub(now)
	if remaining < 0 {
		remaining = 0
	}
	color := ansiYellow
	if remaining < 30*time.Minute {
		color = ansiRed
	}
	return fmt.Sprintf("%s高峰%s %s%s%s", ansiWhite, ansiReset, color, humanDuration(remaining), ansiReset)
}

// freshTrailer: 新鲜度尾标(极简方案用)。多 key 场景:用 default key
// 的 FetchedAt — 任何 Key 刷新成功就刷新整个 trailer,避免 5h 倒计时
// 与 weekly 倒计时各挂各的。
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
	return ansiWhite + fmt.Sprintf("(↻%s)", humanDuration(ttl-age)) + ansiReset
}

// renderStatusline 按配置的 layout 方案渲染;COLUMNS 过窄时自动降级到
// 极简排版,避免终端截断(Claude Code 在运行脚本前设置 COLUMNS/LINES)。
//
//	layout a(默认): ➜ dir git:(main)✗ Model [ctx] 2h50m 81% left weekly 69% left 5d6h (↻3m)
//	layout a(默认,左稳定→中关心→右变动): submon ⎇main  GLM-5.3  ctx 12.3k/200k 6%  xhigh  fast  5h 4h50m 90%  weekly 53% 4d13h  ↻3m
//	layout b(信息优先):                       Model · ctx 已用 15% · dir · ⎇main · 5h 剩 72%（1h9m 后重置） · weekly 剩 74%（5d10h 后重置）
//	layout c(极简):                            dir main · 15% · 72%/74% (↻2m)
func renderStatusline(in *statuslineInput, cfg *config.Config, now time.Time) string {
	modelName := stripANSI(in.Model.Display)

	cwd := in.Workspace.CurrentDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	dir := filepath.Base(cwd)

	layout := cfg.Statusline.Layout
	if layout != "c" && termColumns() < 100 {
		// 窄终端:估算 a/b 排版放不下,自动降级到极简。
		layout = "c"
	}
	switch layout {
	case "b":
		return renderLayoutB(in, cfg, modelName, dir, cwd, now)
	case "c":
		return renderLayoutC(in, cfg, modelName, dir, cwd, now)
	default:
		return renderLayoutA(in, cfg, modelName, dir, cwd, now)
	}
}

// termColumns 读取 Claude Code 注入的终端宽度;读不到返回一个大数(不降级)。
func termColumns() int {
	if v := envInt64("COLUMNS"); v > 0 {
		return int(v)
	}
	return 1 << 30
}

// renderLayoutA 是默认排版:左稳定(目录+分支)→ 模型+effort → fast → ctx
// (max+bar+%)→ 配额。
//
//	submon main GLM-5.3 xhigh fast 200k [▍       ] 6% MINIMAX 3h5m 97% - weekly 99% 5d3h (↻1m)
//
// 设计原则:
//   - 段间单空格分隔(视觉整齐)
//   - effort 紧贴模型(模型属性);fast 单独成段(运行开关,语义不同)
//   - 视觉锚点居中:ctx(max + 进度条 + %)是用户最关心的信息
//   - 减特殊符号:无 ➜、⚡、⎇、✗;只保留 *(脏,git status 通用)和 ↻(新鲜度)
func renderLayoutA(in *statuslineInput, cfg *config.Config, modelName, dir, cwd string, now time.Time) string {
	var segs []string

	// 1. 左:目录 + 分支(纯文本,无 ⎇;颜色区分 dir/branch)。
	if dir != "" {
		seg := ansiCyan + dir + ansiReset
		if branch, dirty := gitInlineSegment(cwd); branch != "" {
			seg += " " + ansiMagenta + branch + ansiReset
			if dirty {
				seg += ansiYellow + "*" + ansiReset
			}
		}
		segs = append(segs, seg)
	}

	// 2. 模型 + effort(紧贴,中间单空格)—— effort 是模型属性,不是会话开关。
	var modelSeg string
	if modelName != "" {
		modelSeg = ansiCyan + modelName + ansiReset
	}
	if e := effortLabel(in); e != "" {
		if modelSeg != "" {
			modelSeg += " " + e
		} else {
			modelSeg = e
		}
	}
	if modelSeg != "" {
		segs = append(segs, modelSeg)
	}

	// 3. fast 模式(运行开关,单独成段;只在开启时显示)。
	if f := fastLabel(in); f != "" {
		segs = append(segs, f)
	}

	// 4. 中部锚点:token 使用进度(max + bar + %)。
	if ctx := contextSegment(in.ContextWindow); ctx != "" {
		segs = append(segs, ctx)
	}

	// 5. 最右:配额(变化最频繁)。
	if q := quotaSegmentFor(in, cfg, now); q != "" {
		segs = append(segs, q)
	}

	// 6. 高峰期提醒(Zhipu 专属,窗口内显示)。
	if ph := peakHourReminder(cfg, now); ph != "" {
		segs = append(segs, ph)
	}

	// 单空格分隔:视觉整齐,不让分隔符抢色。
	return strings.Join(segs, " ")
}

// effortLabel 返回 effort 档位文本(magenta);空字符串=不显示。
// 任何档位都可见 —— docs/statusline.md 明确该字段反映 mid-session 变化。
func effortLabel(in *statuslineInput) string {
	switch in.Effort.Level {
	case "low", "medium", "high", "xhigh", "max":
		return ansiMagenta + in.Effort.Level + ansiReset
	}
	return ""
}

// fastLabel 返回 fast mode 文本(yellow,纯文本无 emoji);空=不显示。
func fastLabel(in *statuslineInput) string {
	if !in.FastMode {
		return ""
	}
	return ansiYellow + "fast" + ansiReset
}

// quotaSegmentFor 选择配额段来源:官方模式且 stdin 带 rate_limits 时,
// 直接用官方数据(零 API、零缓存);否则回落到订阅 provider 缓存。
//
// provider 选择**只看** --provider / $ANTHROPIC_BASE_URL(走
// resolveStatuslineProvider)。resolve 失败就不显示 — 不静默挑缓存里
// "最近有数据的 provider",也不靠模型名猜 — 这两种都是错的:
//   - cache 兜底:本地代理(127.0.0.1)时,会拿之前 Kimi 会话留下的
//     缓存冒充当前会话,误导。
//   - 模型名猜:claude / kimi-k2 这种命名交错时必然命中错对象。
//
// 想看非当前会话的 provider,用 `submon status <name>` 或 `submon statusline --provider <name>`。
func quotaSegmentFor(in *statuslineInput, cfg *config.Config, now time.Time) string {
	if isOfficialClaude() {
		if q := claudeQuotaSegment(in, now); q != "" {
			return q
		}
	}
	return quotaSegment(cfg, now)
}

// claudeQuotaSegment 从 stdin 的 rate_limits 渲染官方订阅限额段。
// 沿用统一窗口形态:5h 倒计时打头,weekly 标签打头。
func claudeQuotaSegment(in *statuslineInput, now time.Time) string {
	cfg := config.Default()
	var parts []string
	if w := in.RateLimits.FiveHour; w.UsedPercentage != nil {
		win := rateLimitToWindow(w, "5h")
		parts = append(parts, colorizeWindow("5h", win, now, cfg))
	}
	if w := in.RateLimits.SevenDay; w.UsedPercentage != nil {
		win := rateLimitToWindow(w, "week")
		parts = append(parts, colorizeWindow("weekly", win, now, cfg))
	}
	if len(parts) == 0 {
		return ""
	}
	return strings.Join(parts, ansiWhite+" - "+ansiReset)
}

// rateLimitToWindow 把 stdin 的官方限额窗口转成 model.Window(剩余语义)。
func rateLimitToWindow(w rateLimitWindow, label string) *model.Window {
	win := &model.Window{Label: label}
	rem := 100 - *w.UsedPercentage
	used := *w.UsedPercentage
	win.RemainingPct = &rem
	win.UsedPct = &used
	if w.ResetsAt != nil {
		t := time.Unix(*w.ResetsAt, 0)
		win.ResetAt = &t
	}
	return win
}

// renderLayoutB: 信息优先 —— model 打头,细粒度分段,branch 用 ⎇ 符号。
// Model · ctx 已用 15% · dir · ⎇main · 5h 剩 72%（1h9m 后重置） · weekly 剩 74%（5d10h 后重置）
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
		segs = append(segs, ansiWhite+"⎇"+ansiReset+ansiMagenta+branch+dirtyMark+ansiReset)
	}
	if q := quotaDetailedSegment(cfg, now); q != "" {
		segs = append(segs, q)
	}
	if ph := peakHourReminder(cfg, now); ph != "" {
		segs = append(segs, ph)
	}
	sep := ansiWhite + " · " + ansiReset
	return strings.Join(segs, sep)
}

// renderLayoutC: 极简 —— 只留 dir、branch、上下文%、两个配额%。
// dir main · 15% · 72%/74% (↻2m)
func renderLayoutC(in *statuslineInput, cfg *config.Config, modelName, dir, cwd string, now time.Time) string {
	var segs []string
	segs = append(segs, ansiCyan+dir+ansiReset)
	if branch, err := gitBranch(cwd); err == nil && branch != "" {
		segs = append(segs, ansiWhite+branch+ansiReset)
	}
	// 上下文%:用量越高越红(与配额剩余色互补,阈值共用)。
	if in.ContextWindow.UsedPercentage != nil {
		used := float64(*in.ContextWindow.UsedPercentage)
		segs = append(segs, fmt.Sprintf("%s%.0f%%%s", ctxUsedColor(used), used, ansiReset))
	} else if in.ContextWindow.TotalInputTokens != nil && in.ContextWindow.ContextWindowSize != nil && *in.ContextWindow.ContextWindowSize > 0 {
		effective := *in.ContextWindow.ContextWindowSize
		if v := envInt64("CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v > 0 && v < effective {
			effective = v
		}
		if v := envInt64("CLAUDE_CODE_AUTO_COMPACT_WINDOW"); v > 0 && v < effective {
			effective = v
		}
		pct := float64(*in.ContextWindow.TotalInputTokens) / float64(effective) * 100
		segs = append(segs, fmt.Sprintf("%s%.0f%%%s", ctxUsedColor(pct), pct, ansiReset))
	}
	if isOfficialClaude() {
		if q := claudeMinimalQuota(in); q != "" {
			segs = append(segs, q)
			sep := ansiWhite + " · " + ansiReset
			return strings.Join(segs, sep)
		}
	}
	if q := quotaMinimalSegment(cfg, now); q != "" {
		segs = append(segs, q)
	}
	if ph := peakHourReminder(cfg, now); ph != "" {
		segs = append(segs, ph)
	}
	sep := ansiWhite + " · " + ansiReset
	return strings.Join(segs, sep)
}

// claudeMinimalQuota: 极简排版下的官方限额 "12%/34%"(剩余%)。
func claudeMinimalQuota(in *statuslineInput) string {
	var segs []string
	for _, w := range []rateLimitWindow{in.RateLimits.FiveHour, in.RateLimits.SevenDay} {
		if w.UsedPercentage == nil {
			continue
		}
		rem := 100 - *w.UsedPercentage
		segs = append(segs, fmt.Sprintf("%s%.0f%%%s", pctColor(rem, config.Default()), rem, ansiReset))
	}
	if len(segs) == 0 {
		return ""
	}
	return strings.Join(segs, ansiWhite+"/"+ansiReset)
}

// ctxUsedColor 上下文已用%配色:>80 红,>60 黄,其余绿。
// (与配额"剩余%"阈值方向相反:这里越高越危险。)
func ctxUsedColor(used float64) string {
	switch {
	case used > 80:
		return ansiRed
	case used > 60:
		return ansiYellow
	}
	return ansiGreen
}

// gitInlineSegment 返回分支名和脏标记。无 ⎇、git:() 等装饰字符
// —— 全部由 renderLayoutA 用颜色区分。无 ANSI 包装,返回纯文本。
func gitInlineSegment(cwd string) (branch string, dirty bool) {
	b, err := gitBranch(cwd)
	if err != nil || b == "" {
		return "", false
	}
	d, _ := gitDirty(cwd)
	return b, d
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

// contextSegment 渲染 "200k [▍░░░░░░░] 6%"。
//
// 顺序:最大上下文 → 进度条 → 百分比(用户希望的排列)。最大上下文用
// humanTokens 紧凑显示(200k / 1M);进度条 8 格宽度 + 8 分块字符(精度
// 约 1.5%);百分比阈值染色,bar 用相同色。
//
// 两个 Claude Code 环境变量会修正"实际上下文上限":
//   - CLAUDE_CODE_MAX_CONTEXT_TOKENS: 覆盖 Claude Code 对当前模型上下文
//     窗口大小的假设(经网关路由、窗口与内置假设不符时使用)
//   - CLAUDE_CODE_AUTO_COMPACT_WINDOW: 直接决定 auto-compact 触发点
//     (100K–1M,例如 500000 = 对话到 500K 时压缩)
//
// 有效上限 = min(AUTO_COMPACT_WINDOW, 实际窗口, MAX_CONTEXT_TOKENS)。
func contextSegment(cw contextWindowInfo) string {
	if cw.TotalInputTokens == nil || cw.ContextWindowSize == nil || *cw.ContextWindowSize <= 0 {
		return ""
	}
	total := *cw.ContextWindowSize
	effective := total
	if v := envInt64("CLAUDE_CODE_MAX_CONTEXT_TOKENS"); v > 0 && v < effective {
		effective = v
	}
	if v := envInt64("CLAUDE_CODE_AUTO_COMPACT_WINDOW"); v > 0 && v < effective {
		effective = v
	}
	pct := float64(*cw.TotalInputTokens) / float64(effective) * 100
	color := ctxUsedColor(pct)
	bar := ctxBar(pct)
	return color + humanTokens(float64(effective)) + ansiReset + " " +
		color + "[" + bar + "]" + ansiReset + " " +
		color + fmt.Sprintf("%.0f%%", pct) + ansiReset
}

// ctxBar 渲染 N 格宽度的进度条,使用 8 分块 unicode block 字符(精度
// ≈ 1/(width*8))。pct 范围 [0,100],超界截断。
//
//	'▏▎▍▌▋▊▉█' 七个等级(1/8~8/8 已用),不足 1/8 用 ASCII 空格填充
//	width=8 时:6% → "▍       ",50% → "████    ",75% → "██████  ",100% → "████████"
func ctxBar(pct float64) string {
	const width = 8
	if pct < 0 {
		pct = 0
	}
	if pct > 100 {
		pct = 100
	}
	eighths := pct / 100 * float64(width) * 8
	full := int(eighths / 8)
	rem := int(eighths) % 8
	// index 0 = 1/8 已用(index 0 留空,1/8 以下用空格填充)
	halfBlocks := []rune{' ', '▏', '▎', '▍', '▌', '▋', '▊', '▉'}

	var b strings.Builder
	for i := 0; i < width; i++ {
		r := rune(' ')
		switch {
		case i < full:
			r = '█'
		case i == full && rem > 0:
			r = halfBlocks[rem]
		}
		b.WriteRune(r)
	}
	return b.String()
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

// quotaSegment 读缓存渲染配额段。provider 选择仅来自 --provider /
// ANTHROPIC_BASE_URL(走 resolveStatuslineProvider),不靠模型名猜。
//
// 缓存过期时触发自愈:fork 一个后台静默 watch 刷新(不阻塞本次渲染),
// 受 RefreshedAt 节流,失败也不会打爆 API。
func quotaSegment(cfg *config.Config, now time.Time) string {
	name, err := resolveStatuslineProvider(cfg)
	if err != nil {
		return "" // quotas are an optional segment; never break the line
	}
	return quotaSegmentForProvider(cfg, name, now)
}

// quotaSegmentForProvider 渲染指定 provider 的配额段(读缓存)。
// 多 key 场景:该 provider 配置了 N 个 Key 时,N 个段并列渲染,各自
// 带 (↻) 新鲜度;单 key 场景与历史一致(不显示 label)。
func quotaSegmentForProvider(cfg *config.Config, name model.Provider, now time.Time) string {
	path, err := cfg.CachePath()
	if err != nil {
		return ""
	}
	file, err := cache.New(path).Load()
	if err != nil {
		return ""
	}

	keys := cfg.ProviderKeys(name)
	// env 兜底 / 单 key 老路径:keys 为空但 provider 已配 — 用 default 渲染。
	if len(keys) == 0 {
		keys = []config.Account{{Label: "default"}}
	}

	var segs []string
	for _, k := range keys {
		entry := file.GetLabeled(name, k.Label)
		if entry == nil || entry.Snapshot == nil {
			segs = append(segs, quotaEmptySegment(name, k.Label, entry, cfg, path, now))
			continue
		}
		segs = append(segs, quotaSegmentForEntry(name, k.Label, entry, cfg, now))
	}
	// 多 key 时用 " | " 分隔(段内窗口已用 " - " 分隔),保持视觉对齐。
	return strings.Join(segs, ansiWhite+" | "+ansiReset)
}

// quotaEmptySegment 在缓存缺失时生成一段"刷新中"提示。沿用单 key 老
// 体验,只在 --show-provider 或多 key 时显示 label。
func quotaEmptySegment(name model.Provider, label string, entry *cache.Entry, cfg *config.Config, cachePath string, now time.Time) string {
	maybeSelfHeal(cachePath, entry, cfg, now)
	hint := "暂无数据,刷新中…"
	if entry != nil && entry.LastErr != "" {
		hint = entry.LastErr
	}
	labelPart := ""
	if label != "" && label != "default" {
		labelPart = "/" + label
	}
	badge := ""
	if statuslineShowProvider {
		badge = ansiWhite + badgeText(name) + labelPart + " " + ansiReset
	}
	return badge + ansiWhite + labelPart + " " + hint + ansiReset
}

// quotaSegmentForEntry 渲染单条 Entry 的配额段(单 key 完整版)。
func quotaSegmentForEntry(name model.Provider, label string, entry *cache.Entry, cfg *config.Config, now time.Time) string {
	s := entry.Snapshot
	ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second
	if now.Sub(s.FetchedAt) > ttl {
		maybeSelfHeal("", entry, cfg, now)
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
	if statuslineShowProvider {
		out = providerBadge(s.Provider) + " "
	}
	// 多 key 场景:label 非空且非 default 时,贴在段首(数字前)。
	if label != "" && label != "default" {
		out += ansiMagenta + label + ansiReset + " "
	}
	out += strings.Join(parts, ansiWhite+" - "+ansiReset)

	age := now.Sub(s.FetchedAt)
	switch {
	case age > ttl:
		out += ansiYellow + fmt.Sprintf(" (刷新中·%s前)", humanDuration(age)) + ansiReset
	default:
		next := ttl - age
		out += ansiWhite + fmt.Sprintf(" (↻%s)", humanDuration(next)) + ansiReset
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
	if entry != nil && entry.Snapshot != nil {
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
	cmd.SysProcAttr = detachProcAttr()
	_ = cmd.Start()
	// 父进程(statusline)立即退出;子进程按平台规则脱离父进程
	// (POSIX 由 init 收养,Windows 不挂控制台,见 procattr_*.go),
	// 刷新完写缓存后自行退出。
}

// badgeText is the plain (uncolored) provider label.
func badgeText(p model.Provider) string { return strings.ToUpper(string(p)) }

// providerBadge 渲染 provider tag。**默认情况下不显示**(见
// statuslineShowProvider / --show-provider),仅在调试 / 多 provider 排查
// 时打开。打开时走 ansiWhite — 辅助信息不动色(CLAUDE.md 约定)。
func providerBadge(p model.Provider) string {
	label := strings.ToUpper(string(p))
	return ansiWhite + label + ansiReset
}

// resolveStatuslineProvider: --provider flag > ANTHROPIC_BASE_URL 自动识别。
//
// 没有这两条时直接返回 error — statusline 默认不显示,不静默猜测某个
// provider(用户可能只有 zhipu 想看,强行给 minimax 会误导)。想看
// 非当前会话的 provider,用 `submon status <provider>` 或 `submon statusline --provider X`。
//
// 检测规则由各 provider 在自己文件里声明(DefaultDetectionRules 聚合),
// 代码层维护,不在 config.toml 暴露给用户。
func resolveStatuslineProvider(cfg *config.Config) (model.Provider, error) {
	if statuslineProvider != "" {
		name := model.Provider(statuslineProvider)
		if !model.ValidProvider(name) {
			return "", fmt.Errorf("unknown provider %q", statuslineProvider)
		}
		return name, nil
	}
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		if name, err := provider.DetectFromBaseURL(base, nil); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot resolve provider (set --provider or ANTHROPIC_BASE_URL)")
}

// colorizeWindow 渲染一个窗口段,两种形态:
//
//	short(5h):      "3h8m 86%"           —— 倒计时打头(紧迫感)
//	long(weekly):   "weekly 70% 5d7h"    —— 标签打头,倒计时收尾
//
// 辅助文本(倒计时/标签)用正常前景色,百分比保持阈值变色;
// 不再加 "left" 描述——百分比语义已自明(剩余%),加文字属于冗余字符。
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

	// 5h 窗口:倒计时在前(紧迫感),百分比在后。
	if w.Label == "5h" && w.ResetAt != nil {
		return ansiWhite + humanDuration(time.Until(*w.ResetAt)) + ansiReset + " " +
			color + pctStr + ansiReset
	}
	// weekly/其他:标签 + 百分比 + 倒计时收尾。
	out := ansiWhite + prefix + ansiReset + " " + color + pctStr + ansiReset
	if w.ResetAt != nil {
		out += " " + ansiWhite + humanDuration(time.Until(*w.ResetAt)) + ansiReset
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
	statuslineCmd.Flags().StringVar(&statuslineProvider, "provider", "", "要展示的 provider: "+providerListHint())
	statuslineCmd.Flags().BoolVar(&statuslineShowProvider, "show-provider", false, "在配额段前显示 provider badge(默认关,辅助信息用纯白)")
	// 环境变量等价于开关 — 临时调试比 --show-provider 方便。
	if v := os.Getenv("SUBMON_SHOW_PROVIDER"); v != "" && v != "0" && v != "false" {
		statuslineShowProvider = true
	}
	rootCmd.AddCommand(statuslineCmd)
}
