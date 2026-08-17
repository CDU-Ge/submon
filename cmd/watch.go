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
	Short: "单次刷新 + 阈值告警(供 systemd timer 调用)",
	Long: "刷新全部 provider、更新共享缓存,并在最低剩余百分比跨越阈值\n" +
		"(warn/crit)或回升时推送 Gotify 通知。设计为每次 timer 触发运行一次。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadDecryptedConfig()
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
			return fmt.Errorf("部分 provider 刷新失败")
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
		s := fmt.Sprintf("%s 窗口: 剩余 %.0f%%", windowLabel(w.Label), *w.RemainingPct)
		if w.ResetAt != nil {
			s += "(" + humanDuration(time.Until(*w.ResetAt)) + "后重置)"
		}
		windows = append(windows, s)
	}
	body := fmt.Sprintf("最低剩余 %.0f%%,当前级别 %s\n%s", pct, level, joinLines(windows))

	switch level {
	case "crit":
		return fmt.Sprintf("submon %s: 配额严重不足", providerCN(name)), body
	case "warn":
		return fmt.Sprintf("submon %s: 配额偏低", providerCN(name)), body
	default:
		return fmt.Sprintf("submon %s: 配额已恢复", providerCN(name)), "配额回升越过阈值。\n" + joinLines(windows)
	}
}

// providerNick 中文 provider 名。
func providerCN(name model.Provider) string {
	if name == model.ProviderZhipu {
		return "智谱"
	}
	return "MiniMax"
}

// windowLabel 窗口中文标签。
func windowLabel(l string) string {
	switch l {
	case "5h":
		return "5小时"
	case "week":
		return "每周"
	case "month":
		return "每月"
	}
	return l
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
