package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
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
// contextWindowInfo 是 Claude Code stdin 里的 context_window 对象。官方语义
// (docs/statusline.md「Context window fields」)决定了下面的取值优先级:
//   - used_percentage 是 Claude Code 自己算好的已用%,与 /context 同源 —— 首选;
//   - total_input_tokens 在首次 API 响应之前、以及 /compact 之后到下一次响应
//     之间都是 0 —— 那是"这一帧没有数据",不是"用量真的是 0"。
//
// 数值一律 float64:文档写的是整数,但小数(8.5)也解析得动,免得一个类型
// 不匹配就把整段上下文吞掉。
type contextWindowInfo struct {
	TotalInputTokens  *float64 `json:"total_input_tokens"`
	ContextWindowSize *float64 `json:"context_window_size"`
	UsedPercentage    *float64 `json:"used_percentage"`
}

// rateLimitWindow 是 Claude Code stdin 里的官方订阅限额窗口。
type rateLimitWindow struct {
	UsedPercentage *float64 `json:"used_percentage"`
	ResetsAt       *int64   `json:"resets_at"` // epoch 秒
}

type statuslineInput struct {
	// SessionID 会话内稳定、跨会话唯一 —— 上下文快照的缓存键(官方文档
	// 在"缓存慢操作"一节同样推荐用它做状态栏缓存键)。
	SessionID string `json:"session_id"`
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

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "渲染 Claude Code 状态栏",
	Long: "输出单行状态栏:左稳定(目录/git 分支/模型)→ 中关心(token 使用)\n" +
		"→ 右变动(effort/fast/订阅配额)。只读缓存,不联网。\n" +
		"provider 仅由 $ANTHROPIC_BASE_URL 自动识别。\n" +
		"都没有命中 → statusline 不显示配额段,不强行猜 provider。",
	Example: "  # 在 Claude Code settings.json 里:\n" +
		"  submon install              # 一键接入 ~/.claude/settings.json\n\n" +
		"  # 手动调用(不通过 CC):\n" +
		"  echo '{\"model\":{\"display_name\":\"GLM-5.3\"}}' | submon statusline\n" +
		"  submon statusline",
	RunE: func(cmd *cobra.Command, args []string) error {
		timing := newStatuslineTiming(os.Getenv("SUBMON_STATUSLINE_TIMING") == "1")
		defer timing.write(cmd.ErrOrStderr())
		// Claude Code 经管道捕获 statusline 输出并渲染 ANSI — 非 TTY
		// 不作为关色依据。PersistentPreRun 已按 TTY 规则算过一次,
		// 这里翻开关后重算(纯内存运算,秒回预算内)。
		allowPipeColor = true
		refreshColorState()

		stage := timing.begin()
		cfg, err := config.Load()
		if err != nil {
			cfg = config.Default() // statusline must never hard-fail
		}

		timing.end("config", stage)
		stage = timing.begin()
		// Read Claude Code's JSON payload from stdin; absent fields degrade.
		// 绝不阻塞:见 readStdinWithin —— 卡在 stdin 上等于状态栏一行都不出。
		var in statuslineInput
		if !stdinIsTerminal() {
			if raw := readStdinWithin(os.Stdin, statuslineStdinBudget); len(raw) > 0 {
				_ = json.Unmarshal(raw, &in)
			}
		}

		timing.end("stdin", stage)
		stage = timing.begin()
		out := cmd.OutOrStdout()
		fmt.Fprintln(out, renderStatusline(&in, cfg, time.Now()))
		timing.end("render", stage)
		return nil
	},
}

// isOfficialClaude 报告当前会话是否跑在 Claude 官方订阅上(而非网关)。
// 仅此时 stdin 的 rate_limits 字段才存在,也仅此时启用 claude 伪 provider。
func isOfficialClaude() bool {
	base := strings.ToLower(os.Getenv("ANTHROPIC_BASE_URL"))
	return base == "" || strings.Contains(base, "anthropic.com")
}

// statuslineStdinBudget 是读 stdin 的软期限(上限,不是固定等待)。
const statuslineStdinBudget = 200 * time.Millisecond

// stdinIsTerminal:手敲 `submon statusline`(stdin 是终端,永远不会有
// EOF)时直接不用等 —— 交互式运行必须立刻出结果。
func stdinIsTerminal() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return (fi.Mode() & os.ModeCharDevice) != 0
}

// readStdinWithin 读 stdin,但**绝不无限期等待**:到点就返回已读到的字节
// (可能为空),让渲染继续。
//
// 为什么不能直接 io.ReadAll:Claude Code 把 JSON 写进管道后不一定关掉它,
// 而 ReadAll 要读到 EOF 才返回 —— 进程就那么挂着,直到被 Claude Code 的
// 超时杀掉,用户看到的就是"状态栏一行都不出"。反过来,只要把已读到的字节
// 交给上层,即使管道一直开着,payload 也是完整的。
func readStdinWithin(r io.Reader, budget time.Duration) []byte {
	type chunk struct {
		data []byte
		err  error
	}
	ch := make(chan chunk, 1)
	done := make(chan struct{})
	defer close(done)
	// 本函数消费并关闭 stdin/pipe,让阻塞的 Read 在返回时退出。
	if closer, ok := r.(io.Closer); ok {
		defer closer.Close()
	}
	r = io.LimitReader(r, 1<<20) // 异常输入不能无限扩张内存。
	send := func(c chunk) bool {
		select {
		case ch <- c:
			return true
		case <-done:
			return false
		}
	}
	go func() {
		buf := make([]byte, 32*1024)
		for {
			n, err := r.Read(buf)
			if n > 0 {
				if !send(chunk{data: append([]byte(nil), buf[:n]...)}) {
					return
				}
			}
			if err != nil {
				send(chunk{err: err})
				return
			}
		}
	}()

	var out []byte
	timer := time.NewTimer(budget)
	defer timer.Stop()
	for {
		select {
		case c := <-ch:
			out = append(out, c.data...)
			// payload 自洽就立刻收工:CC 写完 JSON 不一定关管道,继续等到期限
			// 等于每帧白等 200ms;而 CC 会取消"还在跑"的状态栏脚本(官方:
			// 新更新触发时 in-flight 脚本被取消),那一帧整行空白 —— 也是跳闪。
			// json.Valid 对半截 payload 返回 false,继续攒下一块。
			if c.err != nil || json.Valid(out) {
				return out // EOF / 出错,或 payload 已完整
			}
		case <-timer.C:
			// defer 关闭管道并通知发送方退出。
			return out
		}
	}
}

// contextPlainSegment: layout B 的上下文段 "ctx 已用 15%"。
func contextPlainSegment(in *statuslineInput, cfg *config.Config, now time.Time) string {
	pct, _, ok := ctxUsage(in, cfg, now)
	if !ok {
		return ""
	}
	return fmt.Sprintf("ctx %s已用 %.0f%%%s", ansiGreen, pct, ansiReset)
}

// quotaDetailedSegment: layout B 的配额段 "5h 剩 72%（1h9m 后重置） · weekly 剩 74%（5d10h 后重置）"。
func quotaDetailedSegment(cfg *config.Config, now time.Time) string {
	segs := quotaWindowSegs(cfg, now, true)
	if len(segs) == 0 {
		return ""
	}
	out := strings.Join(segs, ansiWhite+" · "+ansiReset)
	if age := freshTrailer(cfg, now); age != "" {
		out += " " + age
	}
	return out
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
	name, err := resolveStatuslineProvider()
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
			continue
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
	name, err := resolveStatuslineProvider()
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
	// 关键:用入参 now 算差,不是 time.Until()(后者读真实时钟,测试会错位)。
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
	name, err := resolveStatuslineProvider()
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
		return ansiYellow + fmt.Sprintf("(缓存过期·%s前)", humanDuration(age)) + ansiReset
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

	// 目录段只认 Claude Code 给的会话目录(workspace.current_dir / cwd)。
	// 以前这里在缺失时退回 os.Getwd() —— 那是进程的偶然工作目录,不是会话
	// 目录:在 exe 所在目录手动跑一次就会显示 "Tools" 这种凭空出现的段,
	// 既误导用户,又白 spawn 一次 git。没有就不显示目录/分支段。
	cwd := in.Workspace.CurrentDir
	dir := ""
	if cwd != "" {
		dir = filepath.Base(cwd)
	}

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
		if branch, dirty := gitInlineSegment(cfg, cwd, now); branch != "" {
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
	if ctx := contextSegment(in, cfg, now); ctx != "" {
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
// provider 选择**只看** $ANTHROPIC_BASE_URL(走
// resolveStatuslineProvider)。resolve 失败就不显示 — 不静默挑缓存里
// "最近有数据的 provider",也不靠模型名猜 — 这两种都是错的:
//   - cache 兜底:本地代理(127.0.0.1)时,会拿之前 Kimi 会话留下的
//     缓存冒充当前会话,误导。
//   - 模型名猜:claude / kimi-k2 这种命名交错时必然命中错对象。
//
// 想看非当前会话的 provider,用 `submon status <name>`。
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
	if ctx := contextPlainSegment(in, cfg, now); ctx != "" {
		segs = append(segs, ctx)
	}
	if dir != "" {
		segs = append(segs, ansiYellow+dir+ansiReset)
	}
	if branch, dirty := gitStatusCached(cfg, cwd, now); branch != "" {
		dirtyMark := ""
		if dirty {
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
	if dir != "" {
		segs = append(segs, ansiCyan+dir+ansiReset)
	}
	if branch, _ := gitStatusCached(cfg, cwd, now); branch != "" {
		segs = append(segs, ansiWhite+branch+ansiReset)
	}
	// 上下文%:用量越高越红(与配额剩余色互补,阈值共用)。取值走 ctxUsage
	// 统一入口 —— 三套排版必须同源,否则同一帧在 a/b/c 下数字会不一致。
	if pct, _, ok := ctxUsage(in, cfg, now); ok {
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
func gitInlineSegment(cfg *config.Config, cwd string, now time.Time) (branch string, dirty bool) {
	b, d := gitStatusCached(cfg, cwd, now)
	if b == "" {
		return "", false
	}
	return b, d
}

// gitCmd 是渲染路径上唯一的 git 子进程构造入口 —— 统一挂
// hideWindowProcAttr,Windows 上才不会每次渲染都闪出控制台窗口。
func gitCmd(ctx context.Context, args ...string) *exec.Cmd {
	c := exec.CommandContext(ctx, "git", args...)
	c.WaitDelay = 25 * time.Millisecond
	c.SysProcAttr = hideWindowProcAttr()
	return c
}

// gitStatus 一次 spawn 同时取分支名与脏标记。原来是 rev-parse + diff
// 两次 spawn,而 statusline 是高频路径,进程开销远大于解析成本。
//
// 脏标记语义与 `git diff --quiet HEAD` 保持一致:只看 HEAD 跟踪的文件,
// untracked('? ')与 ignored('! ')不算脏 —— 不然构建产物会一直点亮 *。
// 正因为不看 untracked,这里显式 --untracked-files=no:大仓库里免掉整棵
// 工作区的 untracked 扫描(官方文档也点名 git status 是状态栏卡顿主因)。
func gitStatus(cwd string) (branch string, dirty bool, err error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitStatusBudget)
	defer cancel()
	out, err := gitOutput(gitCmd(ctx, "-C", cwd, "-c", "core.fsmonitor=false", "--no-optional-locks", "status", "--porcelain=v2", "--branch", "--untracked-files=no"))
	if err != nil {
		return "", false, err
	}
	for _, line := range strings.Split(string(out), "\n") {
		switch {
		case strings.HasPrefix(line, "# branch.head "):
			branch = strings.TrimSpace(strings.TrimPrefix(line, "# branch.head "))
			if branch == "(detached)" {
				branch = "HEAD" // 与 rev-parse --abbrev-ref 的历史输出对齐
			}
		case line == "" || line[0] == '#':
			// 头部/空行:无信息,跳过。
		case line[0] == '1' || line[0] == '2' || line[0] == 'u':
			dirty = true
		}
	}
	return branch, dirty, nil
}

// Git 查询受短时预算和跨进程缓存节流约束。
const (
	gitStatusTTL    = 5 * time.Second
	gitStatusBudget = 150 * time.Millisecond
)

// gitState 是分支段落的磁盘缓存快照。
type gitState struct {
	Branch string    `json:"branch"`
	Dirty  bool      `json:"dirty"`
	At     time.Time `json:"at"`
}

// gitStatusCached 是渲染路径读分支/脏标记的唯一入口。缓存键就是 cwd:
// git 状态本来就按目录算,同一目录跨会话共享缓存只会更省 spawn;反过来,
// 只按 session_id 建键是错的 —— 会话内换目录(workspace.current_dir 会变)
// 会拿到上一个目录的结果。
//
// 为什么必须缓存:Claude Code 自己是 300ms debounce + 事件驱动(每条助手
// 消息、token 数变化、模式切换都会触发),一轮活跃会话里状态栏命令会被叫
// 很多次;而 Windows 上每次 git 要几十毫秒并且要扫工作区。逐帧 spawn 的
// 结果是多个 git 叠着跑,磁盘被打满、整机卡死(官方文档 Tips 也专门警告
// 这一条,并给出 5 秒缓存的做法)。所以:
//   - 结果按 cwd 落盘,TTL 内直接复用;
//   - 抢不到锁 = 已经有进程在跑 git,那就用旧值 —— 绝不并发 spawn。
func gitStatusCached(cfg *config.Config, cwd string, now time.Time) (branch string, dirty bool) {
	if cwd == "" {
		return "", false // 没有会话目录就没有 git 段,连 spawn 都不做
	}
	path := gitCachePath(cfg, cwd)
	if path == "" {
		return "", false // 无法节流时省略 Git 段。
	}
	if st, ok := loadGitState(path); ok && now.Sub(st.At) < gitStatusTTL {
		return st.Branch, st.Dirty
	}
	unlock, locked := tryStatuslineLock(path + ".lock")
	if !locked {
		st, _ := loadGitState(path)
		return st.Branch, st.Dirty
	}
	defer unlock()

	// 获取锁后再读缓存:其他渲染可能刚完成查询。
	st, _ := loadGitState(path)
	if now.Sub(st.At) < gitStatusTTL {
		return st.Branch, st.Dirty
	}
	// 先持久化尝试时间。写失败则不启动子进程;被取消也保留节流。
	st.At = now
	raw, err := json.Marshal(st)
	if err != nil || os.WriteFile(path, raw, 0o600) != nil {
		return st.Branch, st.Dirty
	}
	b, d, err := gitStatus(cwd)
	if err == nil {
		st.Branch, st.Dirty = b, d
	}
	st.At = now // 超时、非仓库、Git 不可用也节流,保留旧值。
	if raw, err := json.Marshal(st); err == nil {
		_ = os.WriteFile(path, raw, 0o600)
	}
	return st.Branch, st.Dirty
}

// gitCachePath:每个 cwd 一份 git 缓存,放在 submon 的缓存目录里。
func gitCachePath(cfg *config.Config, cwd string) string {
	p, err := cfg.CachePath()
	if err != nil || cwd == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(cwd))
	return filepath.Join(filepath.Dir(p), fmt.Sprintf("git-%08x.json", h.Sum32()))
}

func loadGitState(path string) (gitState, bool) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return gitState{}, false
	}
	var st gitState
	if err := json.Unmarshal(raw, &st); err != nil {
		return gitState{}, false
	}
	return st, true
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
//
// 取值(含"瞬时归零"的处理)一律走 ctxUsage,三套排版同源。
func contextSegment(in *statuslineInput, cfg *config.Config, now time.Time) string {
	pct, effective, ok := ctxUsage(in, cfg, now)
	if !ok {
		return ""
	}
	color := ctxUsedColor(pct)
	bar := ctxBar(pct)
	return color + humanTokens(float64(effective)) + ansiReset + " " +
		color + "[" + bar + "]" + ansiReset + " " +
		color + fmt.Sprintf("%.0f%%", pct) + ansiReset
}

// ctxSample 是"上一帧的上下文用量",落盘只为吃掉瞬时归零。
type ctxSample struct {
	Pct       float64   `json:"pct"`
	Effective int64     `json:"eff"`
	At        time.Time `json:"at"`
}

const (
	// ctxStickyTTL 是瞬时归零的保护窗口。Claude Code 在首次 API 响应之前、
	// 以及 /compact 之后到下一次响应之间,把 total_input_tokens / used_percentage
	// 报成 0 或 null(见 docs/statusline.md),真实值几秒内就回来。30s 盖得住
	// 这个空窗,又不至于让 /clear 之后真实的归零迟迟显示不出来。
	ctxStickyTTL = 30 * time.Second
	// ctxWriteMinInterval 是落盘节流:状态栏每帧都跑,这个文件不能变成
	// 新的 I/O 风暴源(与 gitStatusTTL 同级)。
	ctxWriteMinInterval = 5 * time.Second
)

// ctxUsage 是上下文用量唯一的取值入口,三套排版共用。
//
// 优先级:
//  1. used_percentage —— Claude Code 自己算好的已用%,官方推荐,与 /context 一致;
//  2. total_input_tokens / 有效上限 —— 上一项缺失时的兜底;
//  3. 上一帧的值(ctxStickyTTL 内)—— 前两样都没有时用它,消掉跳闪;
//  4. 都没有 → ok=false,整段不渲染。
//
// 关键:payload 里的 0 一律按"这一帧没有数据"处理。真实用量不会恰好是 0,
// 而官方文档明确 0 是首帧/压缩后的中间态 —— 这正是状态栏数字跳到 0 又跳回
// 来的原因(跳闪)。
func ctxUsage(in *statuslineInput, cfg *config.Config, now time.Time) (pct float64, effective int64, ok bool) {
	cw := in.ContextWindow

	// 有效上限 = min(窗口大小, MAX_CONTEXT_TOKENS, AUTO_COMPACT_WINDOW)。
	eff := int64(0)
	if cw.ContextWindowSize != nil && *cw.ContextWindowSize > 0 {
		eff = int64(*cw.ContextWindowSize)
	}
	for _, v := range []int64{envInt64("CLAUDE_CODE_MAX_CONTEXT_TOKENS"), envInt64("CLAUDE_CODE_AUTO_COMPACT_WINDOW")} {
		if v > 0 && (eff <= 0 || v < eff) {
			eff = v
		}
	}

	switch {
	case cw.UsedPercentage != nil && *cw.UsedPercentage > 0:
		pct = *cw.UsedPercentage
	case cw.TotalInputTokens != nil && *cw.TotalInputTokens > 0 && eff > 0:
		pct = *cw.TotalInputTokens / float64(eff) * 100
	default:
		pct = -1 // 这一帧没有可用数据:首帧之前,或压缩进行中
	}

	path := ctxCachePath(cfg, in.SessionID, in.Workspace.CurrentDir)
	stored, have := loadCtxSample(path)

	if pct < 0 {
		if eff <= 0 {
			eff = stored.Effective
		}
		if have && stored.Pct > 0 && now.Sub(stored.At) < ctxStickyTTL {
			return clampPct(stored.Pct), eff, eff > 0
		}
		// 没有可借的上一帧(会话刚开始):渲染 0% 是诚实的。
		return 0, eff, eff > 0
	}

	if eff <= 0 {
		return 0, 0, false
	}
	pct = clampPct(pct)
	saveCtxSample(path, ctxSample{Pct: pct, Effective: eff, At: now}, stored, have, now)
	return pct, eff, true
}

// clampPct 把百分比收进 [0,100] —— 上游偶尔会给 >100(缓存 token 语义差)。
func clampPct(p float64) float64 {
	if p < 0 {
		return 0
	}
	if p > 100 {
		return 100
	}
	return p
}

// ctxCachePath 每个会话一份上下文快照,和 git 缓存同目录、同样的哈希键。
// 键优先用 session_id(会话内稳定、跨会话唯一),老版本 Claude Code 不给时
// 退回 cwd;两个都没有就不落盘。
func ctxCachePath(cfg *config.Config, sessionID, cwd string) string {
	p, err := cfg.CachePath()
	if err != nil {
		return ""
	}
	key := sessionID
	if key == "" {
		key = cwd
	}
	if key == "" {
		return ""
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return filepath.Join(filepath.Dir(p), fmt.Sprintf("ctx-%08x.json", h.Sum32()))
}

func loadCtxSample(path string) (ctxSample, bool) {
	if path == "" {
		return ctxSample{}, false
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ctxSample{}, false
	}
	var s ctxSample
	if err := json.Unmarshal(raw, &s); err != nil {
		return ctxSample{}, false
	}
	return s, true
}

// saveCtxSample 落盘当前值。节流:同一会话每 ctxWriteMinInterval 最多写一次,
// 只有有效上限变了(换模型/换窗口)才提前写。
func saveCtxSample(path string, s, prev ctxSample, have bool, now time.Time) {
	if path == "" {
		return
	}
	if have && prev.Effective == s.Effective && now.Sub(prev.At) < ctxWriteMinInterval {
		return
	}
	raw, err := json.Marshal(s)
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(path), 0o700)
	_ = os.WriteFile(path, raw, 0o600)
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

// quotaSegment 读缓存渲染配额段。provider 选择仅来自
// ANTHROPIC_BASE_URL(走 resolveStatuslineProvider),不靠模型名猜。
//
// 缓存更新由定时 watch 负责;渲染绝不启动后台刷新。
func quotaSegment(cfg *config.Config, now time.Time) string {
	name, err := resolveStatuslineProvider()
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
			segs = append(segs, quotaEmptySegment(k.Label, entry))
			continue
		}
		segs = append(segs, quotaSegmentForEntry(k.Label, entry, cfg, now))
	}
	// 多 key 时用 " | " 分隔(段内窗口已用 " - " 分隔),保持视觉对齐。
	return strings.Join(segs, ansiWhite+" | "+ansiReset)
}

// quotaEmptySegment 在缓存缺失时提示显式刷新。沿用单 key 老
// 体验,多 key 时显示 label。
func quotaEmptySegment(label string, entry *cache.Entry) string {
	hint := "暂无数据,请运行 submon watch"
	if entry != nil && entry.LastErr != "" {
		hint = entry.LastErr
	}
	labelPart := ""
	if label != "" && label != "default" {
		labelPart = "/" + label
	}
	return ansiWhite + labelPart + " " + hint + ansiReset
}

// quotaSegmentForEntry 渲染单条 Entry 的配额段(单 key 完整版)。
// 过期数据继续展示,并标记缓存年龄。
func quotaSegmentForEntry(label string, entry *cache.Entry, cfg *config.Config, now time.Time) string {
	s := entry.Snapshot
	ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second

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
	// 多 key 场景:label 非空且非 default 时,贴在段首(数字前)。
	if label != "" && label != "default" {
		out += ansiMagenta + label + ansiReset + " "
	}
	out += strings.Join(parts, ansiWhite+" - "+ansiReset)

	age := now.Sub(s.FetchedAt)
	switch {
	case age > ttl:
		out += ansiYellow + fmt.Sprintf(" (缓存过期·%s前)", humanDuration(age)) + ansiReset
	default:
		next := ttl - age
		out += ansiWhite + fmt.Sprintf(" (↻%s)", humanDuration(next)) + ansiReset
	}
	return out
}

// resolveStatuslineProvider 仅根据会话的 ANTHROPIC_BASE_URL 识别 provider。
// 未识别时省略配额;查看其他 provider 使用 submon status <provider>。
func resolveStatuslineProvider() (model.Provider, error) {
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		if name, err := provider.DetectFromBaseURL(base, nil); err == nil {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot resolve provider from ANTHROPIC_BASE_URL")
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
	rootCmd.AddCommand(statuslineCmd)
}
