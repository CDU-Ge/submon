package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/spf13/cobra"
)

// statusline is designed to be wired into Claude Code's statusLine setting.
// It is invoked on nearly every UI render, so it must:
//   - never touch the network (cache-only),
//   - always print one line fast, even when data is stale or missing,
//   - auto-pick the provider matching the session's ANTHROPIC_BASE_URL.
type statuslineInput struct {
	Model struct {
		Display string `json:"display_name"`
	} `json:"model"`
}

var statuslineProvider string

var statuslineCmd = &cobra.Command{
	Use:    "statusline",
	Hidden: false,
	Short:  "One-line cache-only output for terminal statuslines",
	Long: "Print a compact quota line (e.g. \"⚡5h 99%·4h56m │ 周 80%·5d\").\n" +
		"Reads the cache only — run `submon status` or the watch timer to populate it.\n" +
		"Provider pick order: --provider > ANTHROPIC_BASE_URL detection > config default.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			cfg = config.Default() // statusline must never hard-fail
		}

		// Read Claude Code's JSON payload (model info) if present; ignore errors.
		var in statuslineInput
		if raw, err := io.ReadAll(os.Stdin); err == nil && len(raw) > 0 {
			_ = json.Unmarshal(raw, &in)
		}

		name, err := resolveStatuslineProvider(cfg)
		if err != nil {
			fmt.Fprintln(cmd.OutOrStdout(), "submon: no provider")
			return nil
		}

		path, err := cfg.CachePath()
		if err != nil {
			fmt.Fprintln(cmd.OutOrStdout(), "submon: no cache path")
			return nil
		}
		file, err := cache.New(path).Load()
		if err != nil {
			fmt.Fprintln(cmd.OutOrStdout(), "submon: cache unreadable")
			return nil
		}
		entry := file.Get(name)
		if entry == nil || entry.Snapshot == nil {
			hint := "run `submon status --force`"
			if entry != nil && entry.LastErr != "" {
				hint = entry.LastErr
			}
			fmt.Fprintf(cmd.OutOrStdout(), "%s: no data (%s)", name, hint)
			return nil
		}

		fmt.Fprintln(cmd.OutOrStdout(), renderStatusline(entry.Snapshot, time.Now(), cfg))
		return nil
	},
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

// renderStatusline formats the compact one-liner. Colors follow remaining
// percentage: >=30 dim/green, 10-30 yellow, <10 red (ANSI, harmless when the
// terminal strips them).
func renderStatusline(s *model.Snapshot, now time.Time, cfg *config.Config) string {
	const (
		ansiReset = "\033[0m"
		ansiGreen = "\033[32m"
		ansiYellow = "\033[33m"
		ansiRed = "\033[31m"
	)
	pctColor := func(pct float64) string {
		switch {
		case pct < cfg.Notify.CritBelowPct:
			return ansiRed
		case pct < cfg.Notify.WarnBelowPct:
			return ansiYellow
		default:
			return ansiGreen
		}
	}

	var parts []string
	if s.ShortWindow != nil {
		parts = append(parts, colorizeWindow("⚡5h", s.ShortWindow, now, pctColor))
	}
	if s.LongWindow != nil {
		parts = append(parts, colorizeWindow("W", s.LongWindow, now, pctColor))
	}
	if len(parts) == 0 {
		if s.MonthlyToolWindow != nil {
			parts = append(parts, colorizeWindow("M", s.MonthlyToolWindow, now, pctColor))
		} else {
			return "submon: no windows"
		}
	}
	line := strings.Join(parts, " │ ")

	age := now.Sub(s.FetchedAt)
	if age > staleAfter(cfg) {
		line += fmt.Sprintf(" (stale %s)", humanDuration(age))
	}
	return line
}

func colorizeWindow(prefix string, w *model.Window, now time.Time, pctColor func(float64) string) string {
	const (
		reset = "\033[0m"
		dim = "\033[2m"
	)
	if w.RemainingPct == nil {
		return prefix + " n/a"
	}
	out := prefix + " "
	color := pctColor(*w.RemainingPct)
	out += color + fmt.Sprintf("%.0f%%", *w.RemainingPct) + reset
	if w.ResetAt != nil {
		out += dim + "·" + humanDuration(time.Until(*w.ResetAt)) + reset
	}
	return out
}

// staleAfter: twice the TTL — beyond that we flag the line as stale.
func staleAfter(cfg *config.Config) time.Duration {
	return 2 * time.Duration(cfg.Cache.TTLSeconds) * time.Second
}

func init() {
	statuslineCmd.Flags().StringVar(&statuslineProvider, "provider", "", "provider to display (zhipu|minimax)")
	rootCmd.AddCommand(statuslineCmd)
}
