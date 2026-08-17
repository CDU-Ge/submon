package cmd

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/notify"
	"github.com/ge/subscription-monitor/internal/refresh"
	"github.com/spf13/cobra"
)

// watch is a one-shot job meant to be launched by a systemd user timer every
// few minutes: force-refresh every configured provider, update the cache
// (which also feeds statusline), run the alert state machine and push Gotify
// notifications on level *transitions* only, then exit.
var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "One-shot refresh + threshold alerts (for systemd timers)",
	Long: "Refreshes all configured providers, updates the shared cache, and pushes\n" +
		"a Gotify notification when the lowest remaining percentage crosses a\n" +
		"threshold (warn/crit) or recovers. Designed to run once per timer tick.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		log := slog.New(slog.NewTextHandler(cmd.ErrOrStderr(), &slog.HandlerOptions{
			Level: slog.LevelInfo,
		}))

		names, err := targetProviders(nil, cfg)
		if err != nil {
			return err
		}

		g := &notify.Gotify{URL: cfg.Notify.Gotify.URL, AppToken: cfg.Notify.Gotify.AppToken}
		ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
		defer cancel()

		var failed bool
		for _, name := range names {
			res := refresh.One(ctx, name, cfg, log)
			if res.Err != nil {
				failed = true
				continue
			}
			if err := runAlertMachine(ctx, cfg, g, name, res.Snapshot, log); err != nil {
				log.Error("alert machine", "provider", name, "err", err)
			}
		}
		if failed {
			return fmt.Errorf("one or more providers failed to refresh")
		}
		return nil
	},
}

// runAlertMachine compares the snapshot against the persisted per-provider
// alert state and pushes only on transitions:
//
//	ok → warn  (lowest remaining < warn threshold)
//	warn → crit (< crit threshold)
//	warn/crit → ok (recovered)
func runAlertMachine(ctx context.Context, cfg *config.Config, g *notify.Gotify, name model.Provider, snap *model.Snapshot, log *slog.Logger) error {
	path, err := cfg.CachePath()
	if err != nil {
		return err
	}
	store := cache.New(path)
	file, err := store.Load()
	if err != nil {
		return err
	}
	e := file.Get(name)
	if e == nil {
		e = &cache.Entry{}
	}

	pct := snap.CriticalPct()
	newLevel := "ok"
	if pct != nil {
		switch {
		case *pct < cfg.Notify.CritBelowPct:
			newLevel = "crit"
		case *pct < cfg.Notify.WarnBelowPct:
			newLevel = "warn"
		}
	}
	prev := e.AlertState.Level
	if prev == "" {
		prev = "ok"
	}
	if newLevel == prev {
		return nil // no transition, no notification
	}

	title, msg := formatAlert(name, snap, newLevel, *pct)
	if err := g.Send(title, msg, cfg.Notify.Gotify.Priority); err != nil {
		// Notification failure must not corrupt state: keep the old level so
		// the next tick retries the transition.
		log.Error("gotify send failed; state not advanced", "err", err)
		return err
	}
	e.AlertState = cache.AlertState{Level: newLevel, UpdatedAt: time.Now(), LastMessage: msg}
	file.Set(name, e)
	if err := store.Save(file); err != nil {
		return err
	}
	log.Info("alert transition", "provider", name, "from", prev, "to", newLevel)
	return nil
}

func formatAlert(name model.Provider, snap *model.Snapshot, level string, pct float64) (string, string) {
	var windows []string
	for _, w := range []*model.Window{snap.ShortWindow, snap.LongWindow, snap.MonthlyToolWindow} {
		if w == nil || w.RemainingPct == nil {
			continue
		}
		s := fmt.Sprintf("%s: %.0f%% left", w.Label, *w.RemainingPct)
		if w.ResetAt != nil {
			s += " (resets in " + humanDuration(time.Until(*w.ResetAt)) + ")"
		}
		windows = append(windows, s)
	}
	body := fmt.Sprintf("[%s] lowest remaining %.0f%%\n%s",
		level, pct, joinLines(windows))

	switch level {
	case "crit":
		return fmt.Sprintf("submon %s: quota critical", name), body
	case "warn":
		return fmt.Sprintf("submon %s: quota low", name), body
	default:
		return fmt.Sprintf("submon %s: recovered", name), "Quota back above thresholds.\n" + joinLines(windows)
	}
}

func joinLines(lines []string) string {
	out := ""
	for _, l := range lines {
		out += l + "\n"
	}
	return out
}

func init() {
	rootCmd.AddCommand(watchCmd)
}
