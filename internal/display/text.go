package display

import (
	"fmt"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
)

func HumanDuration(d time.Duration) string {
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

func HumanTokens(v float64) string {
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

func StripANSI(s string) string {
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

func PercentageColor(pct float64, cfg *config.Config, red, yellow, green string) string {
	switch {
	case pct < cfg.Notify.CritBelowPct:
		return red
	case pct < cfg.Notify.WarnBelowPct:
		return yellow
	default:
		return green
	}
}
