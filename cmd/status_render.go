package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

const statusBarWidth = 12

// renderStatus prints the human-rich panel view for one provider:
//
//	ZHIPU · pro  fetched 3m ago
//	────────────────────────────────────────────────
//	  5h          ██████████░░  83% left · 2h29m
//	  weekly      █████████░░░  77% left · 5d11h
//	  monthly     ████████████ 100% left · 15d11h · 1.0K/1.0K
//	  MCP/tools   1.0K / 1.0K remaining
//	  models 24h  GLM-5.3 61.6M · GLM-4.7 122.9K
func renderStatus(w io.Writer, s *model.Snapshot, now time.Time, cfg *config.Config) {
	title := providerBadge(s.Provider)
	if s.PlanLevel != "" {
		title += ansiDim + " · " + ansiReset + s.PlanLevel
	}
	fmt.Fprintf(w, "%s  %s\n", title,
		ansiDim+"fetched "+humanDuration(now.Sub(s.FetchedAt))+" ago"+ansiReset)
	fmt.Fprintln(w, ansiDim+strings.Repeat("─", 52)+ansiReset)

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

// renderStatusWindow draws one quota row: dim label, threshold-colored bar
// and percentage, reset countdown, and counts when the API reports them.
func renderStatusWindow(w io.Writer, label string, win *model.Window, now time.Time, cfg *config.Config) {
	pctStr, bar, color := "  n/a", strings.Repeat("░", statusBarWidth), ansiDim
	if win.RemainingPct != nil {
		pct := *win.RemainingPct
		pctStr = fmt.Sprintf("%3.0f%%", pct)
		color = pctColor(pct, cfg)
		bar = color + miniBar(pct, statusBarWidth) + ansiReset
	}
	line := fmt.Sprintf("  %s%s%s %s %s %s",
		ansiDim, fmt.Sprintf("%-11s", label), ansiReset,
		bar, color+pctStr+ansiReset, ansiDim+"left"+ansiReset)
	if win.ResetAt != nil {
		line += ansiDim + " · " + humanDuration(time.Until(*win.ResetAt)) + ansiReset
	}
	if win.TotalCount != nil && *win.TotalCount > 0 {
		rem := int64(0)
		if win.RemainingCount != nil {
			rem = *win.RemainingCount
		}
		line += ansiDim + " · " + humanInt(rem) + "/" + humanInt(*win.TotalCount) + ansiReset
	}
	fmt.Fprintln(w, line)
}

// renderStatusLine draws a label/value info row (MCP tools, expiry, models...).
func renderStatusLine(w io.Writer, label, value string) {
	fmt.Fprintf(w, "  %s%s%s %s\n", ansiDim, fmt.Sprintf("%-11s", label), ansiReset, value)
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

func miniBar(pct float64, width int) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	return strings.Repeat("█", filled) + strings.Repeat("░", width-filled)
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
