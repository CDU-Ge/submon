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
//	ZHIPU/alice · pro  fetched 3m ago
//	────────────────────────────────────────────────
//	  5h          ██████████░░  83% left · 2h29m
//	  weekly      █████████░░░  77% left · 5d11h
//	  monthly     ████████████ 100% left · 15d11h · 1.0K/1.0K
//	  MCP/tools   1.0K / 1.0K remaining
//	  models 24h  GLM-5.3 61.6M · GLM-4.7 122.9K
//
// 多 key 场景:标题里带 label(如 "ZHIPU/alice"),让用户区分多个账号。
// 单 key 场景:label 是 "default" 或空,标题保持 "ZHIPU" — 与历史一致。
func renderStatus(w io.Writer, s *model.Snapshot, now time.Time, cfg *config.Config) {
	title := providerBadge(s.Provider)
	if lbl := s.KeyLabel; lbl != "" && lbl != "default" {
		title += ansiWhite + "/" + ansiReset + ansiMagenta + lbl + ansiReset
	}
	if s.PlanLevel != "" {
		title += ansiWhite + " · " + ansiReset + s.PlanLevel
	}
	fmt.Fprintf(w, "%s  %s\n", title,
		ansiWhite+"fetched "+humanDuration(now.Sub(s.FetchedAt))+" ago"+ansiReset)
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
		renderStatusLine(w, "expires",
			fmt.Sprintf("%s · %dd", s.SubscriptionExpiry.Format("2006-01-02"), days))
	}
	if len(s.Models) > 0 {
		var parts []string
		for _, m := range s.Models {
			parts = append(parts, m.Name+" "+humanInt(m.TotalTokens))
		}
		renderStatusLine(w, "models 24h", strings.Join(parts, " · "))
	}
	if s.RawWarning != "" {
		renderStatusLine(w, "note", s.RawWarning)
	}
}

// renderStatusWindow draws one quota row: label, threshold-colored fine-grain
// bar and percentage, reset countdown, and counts when the API reports them.
// 条形只到 80% 宽度后接百分比 —— 剩余语义:填充 = 剩余额度。
func renderStatusWindow(w io.Writer, label string, win *model.Window, now time.Time, cfg *config.Config) {
	pctStr, bar, color := "  n/a", strings.Repeat("░", statusBarWidth), ansiWhite
	if win.RemainingPct != nil {
		pct := *win.RemainingPct
		pctStr = fmt.Sprintf("%3.0f%%", pct)
		color = pctColor(pct, cfg)
		bar = color + miniBar(pct, statusBarWidth) + ansiReset
	}
	line := fmt.Sprintf("  %s%s%s %s %s %s",
		ansiWhite, fmt.Sprintf("%-11s", label), ansiReset,
		bar, color+pctStr+ansiReset, ansiWhite+"left"+ansiReset)
	if win.ResetAt != nil {
		line += ansiWhite + " · " + humanDuration(time.Until(*win.ResetAt)) + ansiReset
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
	fmt.Fprintf(w, "  %s%s%s %s\n", ansiWhite, fmt.Sprintf("%-11s", label), ansiReset, value)
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
		return fmt.Sprintf("%s / %s remaining", humanInt(*win.RemainingCount), humanInt(*win.TotalCount))
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
		return "soon"
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
