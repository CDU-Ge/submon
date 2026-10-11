package statusline

import (
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/display"
	"time"
)

func humanDuration(d time.Duration) string { return display.HumanDuration(d) }
func humanTokens(v float64) string         { return display.HumanTokens(v) }
func stripANSI(s string) string            { return display.StripANSI(s) }

func pctColor(pct float64, cfg *config.Config) string {
	return display.PercentageColor(pct, cfg, ansiRed, ansiYellow, ansiGreen)
}
