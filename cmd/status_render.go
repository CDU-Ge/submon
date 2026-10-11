package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

const statusBarWidth = 10

// barPartialFrac: Unicode 八分块字符,把一格进度拆成 8 个粒度。
// 进度条精度 = width × 8;10 格 = 80 档,足够 1% 粒度。
var barPartialFrac = [8]rune{'░', '▁', '▂', '▃', '▄', '▅', '▆', '▇'} // frac 0..7/8(0 用全空格)

// miniBar 渲染精细进度条:满格用 █,最后未满一格用最近的八分块字符。
// 例(width=10, pct=73):███████▎░░  ← 7 满格 + 2/8 块 + 2 空。
func miniBar(pct float64, width int) string {
	if width <= 0 {
		return ""
	}
	cells := pct / 100 * float64(width)
	full := int(cells)
	frac := int((cells - float64(full)) * 8)
	if frac > 7 {
		frac = 7
	}
	if frac < 0 {
		frac = 0
	}
	var b strings.Builder
	for i := 0; i < full && i < width; i++ {
		b.WriteRune('█')
	}
	if full < width && frac > 0 {
		b.WriteRune(barPartialFrac[frac])
		full++ // 部分格占掉了这一格
	}
	for i := full; i < width; i++ {
		b.WriteRune('░')
	}
	return b.String()
}

// renderStatus prints the human-rich panel view for one provider:
//
//	ZHIPU/alice · pro  3m 前更新
//	────────────────────────────────────────────────
//	  5h          ██████████░░  剩 83% · 2h29m 后重置
//	  weekly      █████████░░░  剩 77% · 5d11h 后重置
//	  monthly     ████████████  剩 100% · 15d11h 后重置 · 1.0K/1.0K
//	  MCP/tools   剩余 1.0K / 1.0K
//	  模型用量 24h  GLM-5.3 61.6M · GLM-4.7 122.9K
//
// 多 key 场景:标题里带 label(如 "ZHIPU/alice"),让用户区分多个账号。
// 单 key 场景:label 是 "default" 或空,标题保持 "ZHIPU" — 与历史一致。
//
// 文案口径:窗口名(5h/weekly/MCP)与紧凑时长(2h29m)是数据标签保留原样;
// 解释性成分(前更新/剩/后重置)一律中文。
func renderStatus(w io.Writer, s *model.Snapshot, now time.Time, cfg *config.Config) {
	title := providerBadge(s.Provider)
	if lbl := s.KeyLabel; lbl != "" && lbl != "default" {
		title += ansiWhite + "/" + ansiReset + ansiMagenta + lbl + ansiReset
	}
	if s.PlanLevel != "" {
		title += ansiWhite + " · " + ansiReset + s.PlanLevel
	}
	fmt.Fprintf(w, "%s  %s\n", title,
		ansiWhite+humanDuration(now.Sub(s.FetchedAt))+" 前更新"+ansiReset)
	fmt.Fprintln(w, ansiWhite+strings.Repeat("─", 52)+ansiReset)

	for _, row := range []struct {
		label string
		win   *model.Window
	}{
		{"5h", s.ShortWindow},
		{"weekly", s.LongWindow},
		{"monthly", s.MonthlyToolWindow},
	} {
		if row.win != nil {
			renderStatusWindow(w, row.label, row.win, now, cfg)
		}
	}
	if s.MonthlyToolWindow != nil {
		renderStatusLine(w, "MCP/tools", formatCounts(s.MonthlyToolWindow))
	}
	if s.SubscriptionExpiry != nil {
		days := int(time.Until(*s.SubscriptionExpiry).Hours() / 24)
		renderStatusLine(w, "到期",
			fmt.Sprintf("%s（%d 天）", s.SubscriptionExpiry.Format("2006-01-02"), days))
	}
	if len(s.Models) > 0 {
		var parts []string
		for _, m := range s.Models {
			parts = append(parts, m.Name+" "+humanInt(m.TotalTokens))
		}
		renderStatusLine(w, "模型用量 24h", strings.Join(parts, " · "))
	}
	if s.RawWarning != "" {
		renderStatusLine(w, "备注", s.RawWarning)
	}
}

// labelWidth 估算标签的终端显示宽度:全宽字符(CJK 等)按 2 列计,
// 其余按 1 列 — 足够本面板的中英混排标签对齐。
func labelWidth(s string) int {
	w := 0
	for _, r := range s {
		if r > 0x7F {
			w += 2
		} else {
			w++
		}
	}
	return w
}

// padLabel 右补空格到目标显示宽度。替代 %-Ns(按 rune 数填充):
// 中文标签 rune 少、占列宽,直接用 %-Ns 会把后续列挤歪。
func padLabel(s string, width int) string {
	if n := labelWidth(s); n < width {
		return s + strings.Repeat(" ", width-n)
	}
	return s
}

// renderStatusWindow draws one quota row: label, threshold-colored fine-grain
// bar and percentage, reset countdown, and counts when the API reports them.
// 条形只到 80% 宽度后接百分比 —— 剩余语义:填充 = 剩余额度,"剩"字点明。
func renderStatusWindow(w io.Writer, label string, win *model.Window, now time.Time, cfg *config.Config) {
	pctSeg, bar := ansiWhite+"n/a"+ansiReset, strings.Repeat("░", statusBarWidth)
	if win.RemainingPct != nil {
		pct := *win.RemainingPct
		color := pctColor(pct, cfg)
		pctSeg = color + "剩 " + fmt.Sprintf("%.0f%%", pct) + ansiReset
		bar = color + miniBar(pct, statusBarWidth) + ansiReset
	}
	line := fmt.Sprintf("  %s %s %s",
		ansiWhite+padLabel(label, 11)+ansiReset, bar, pctSeg)
	if win.ResetAt != nil {
		line += ansiWhite + " · " + humanDuration(time.Until(*win.ResetAt)) + " 后重置" + ansiReset
	}
	if win.TotalCount != nil && *win.TotalCount > 0 {
		rem := int64(0)
		if win.RemainingCount != nil {
			rem = *win.RemainingCount
		}
		line += ansiWhite + " · " + humanInt(rem) + "/" + humanInt(*win.TotalCount) + ansiReset
	}
	fmt.Fprintln(w, line)
}

// renderStatusLine draws a label/value info row (MCP tools, expiry, models...).
func renderStatusLine(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %s %s\n", padLabel(label, 11), value)
}

// pctColor maps remaining percentage to threshold color (shared by status
// panels and the statusline).
func pctColor(pct float64, cfg *config.Config) string {
	switch {
	case pct < cfg.Notify.CritBelowPct:
		return ansiRed
	case pct < cfg.Notify.WarnBelowPct:
		return ansiYellow
	default:
		return ansiGreen
	}
}

func formatCounts(win *model.Window) string {
	if win.RemainingCount != nil && win.TotalCount != nil {
		return fmt.Sprintf("剩余 %s / %s", humanInt(*win.RemainingCount), humanInt(*win.TotalCount))
	}
	return "n/a"
}

func humanInt(v int64) string {
	switch {
	case v >= 1_000_000_000:
		return fmt.Sprintf("%.1fB", float64(v)/1_000_000_000)
	case v >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(v)/1_000_000)
	case v >= 1_000:
		return fmt.Sprintf("%.1fK", float64(v)/1_000)
	default:
		return fmt.Sprintf("%d", v)
	}
}

func humanDuration(d time.Duration) string {
	if d < 0 {
		return "即将"
	}
	switch {
	case d >= 24*time.Hour:
		return fmt.Sprintf("%dd%dh", int(d.Hours())/24, int(d.Hours())%24)
	case d >= time.Hour:
		return fmt.Sprintf("%dh%dm", int(d.Hours()), int(d.Minutes())%60)
	case d >= time.Minute:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	default:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	}
}

// providerBadge 用于 status 面板标题;statusline 不展示 provider badge。
func providerBadge(p model.Provider) string {
	return ansiWhite + strings.ToUpper(string(p)) + ansiReset
}
