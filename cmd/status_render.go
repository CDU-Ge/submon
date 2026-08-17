package cmd

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
)

// renderStatus prints the human-rich view for one provider.
func renderStatus(w io.Writer, s *model.Snapshot, now time.Time) {
	title := strings.ToUpper(string(s.Provider))
	if s.PlanLevel != "" {
		title += " · " + s.PlanLevel
	}
	fmt.Fprintln(w, title)
	fmt.Fprintln(w, strings.Repeat("─", 44))

	if s.ShortWindow != nil {
		renderWindow(w, "5h window ", s.ShortWindow, now)
	}
	if s.LongWindow != nil {
		renderWindow(w, "weekly    ", s.LongWindow, now)
	}
	if s.MonthlyToolWindow != nil {
		renderWindow(w, "monthly   ", s.MonthlyToolWindow, now)
		fmt.Fprintf(w, "           MCP/tools: %s\n", formatCounts(s.MonthlyToolWindow))
	}
	if s.SubscriptionExpiry != nil {
		days := int(time.Until(*s.SubscriptionExpiry).Hours() / 24)
		fmt.Fprintf(w, "subscription expires %s (%d days)\n",
			s.SubscriptionExpiry.Format("2006-01-02"), days)
	}
	if len(s.Models) > 0 {
		fmt.Fprintln(w, "models (last 24h):")
		for _, m := range s.Models {
			fmt.Fprintf(w, "  %-12s %s tokens\n", m.Name, humanInt(m.TotalTokens))
		}
	}
	if s.RawWarning != "" {
		fmt.Fprintf(w, "note: %s\n", s.RawWarning)
	}
	fmt.Fprintf(w, "fetched %s\n", s.FetchedAt.Format("15:04:05"))
}

func renderWindow(w io.Writer, label string, win *model.Window, now time.Time) {
	pct := "--%"
	bar := "░░░░░░░░░░"
	if win.RemainingPct != nil {
		pct = fmt.Sprintf("%.0f%%", *win.RemainingPct)
		bar = miniBar(*win.RemainingPct)
	}
	reset := ""
	if win.ResetAt != nil {
		reset = " · resets in " + humanDuration(time.Until(*win.ResetAt))
	}
	fmt.Fprintf(w, "%s %s %s%s\n", label, bar, pct, reset)
}

func miniBar(pct float64) string {
	const width = 10
	filled := int(pct / 100 * width)
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
	default:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
}
