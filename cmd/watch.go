package cmd

import (
	"context"
	"fmt"
	"io"
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
	Short: "单次刷新 + 催用提醒(供 systemd timer 调用)",
	Long: "刷新全部 provider、更新共享缓存;窗口即将重置且剩余额度较多时\n" +
		"推送「抓紧使用」提醒。设计为每次 timer 触发运行一次。",
	RunE: func(cmd *cobra.Command, args []string) error {
		if watchQuiet {
			// 静默模式(statusline 自愈 fork):日志全部丢弃。
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
		}
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

// runAlertMachine 是"催用提醒"状态机,核心语义:通知的不是"配额快没了",
// 而是"你的配额马上要重置了,还剩很多没用,抓紧用":
//
//	expiring   窗口将在 remind_before 内重置,且剩余 ≥ use_above(值得用)
//	consumed   窗口将重置且剩余 < use_above(已经用得差不多了,不用提醒)
//	ok         其他情况(离重置还远)
//
// 仅在状态变化时推送,避免每个 tick 重复轰炸。
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

	newLevel := "ok"
	var expiring *model.Window
	for _, w := range []*model.Window{snap.ShortWindow, snap.LongWindow} {
		if w == nil || w.ResetAt == nil || w.RemainingPct == nil {
			continue
		}
		if time.Until(*w.ResetAt) > cfg.RemindBeforeDuration() {
			continue
		}
		expiring = w
		if *w.RemainingPct >= cfg.Notify.UseAbovePct {
			newLevel = "expiring"
		} else {
			newLevel = "consumed"
		}
		break
	}
	prev := e.AlertState.Level
	if prev == "" {
		prev = "ok"
	}
	if newLevel == prev {
		return nil // 无状态变化,不推送
	}

	title, msg := formatAlert(name, snap, newLevel, expiring)
	if err := g.Send(title, msg, cfg.Notify.Gotify.Priority); err != nil {
		// 推送失败不推进状态,下个 tick 重试。
		log.Error("gotify 推送失败,状态不前进", "err", err)
		return err
	}
	e.AlertState = cache.AlertState{Level: newLevel, UpdatedAt: time.Now(), LastMessage: msg}
	file.Set(name, e)
	if err := store.Save(file); err != nil {
		return err
	}
	log.Info("提醒状态变化", "provider", name, "from", prev, "to", newLevel)
	return nil
}

// formatAlert 生成"催用"通知:窗口即将重置、剩余额度还很多 → 提醒赶紧用。
func formatAlert(name model.Provider, snap *model.Snapshot, level string, expiring *model.Window) (string, string) {
	var windows []string
	for _, w := range []*model.Window{snap.ShortWindow, snap.LongWindow, snap.MonthlyToolWindow} {
		if w == nil || w.RemainingPct == nil {
			continue
		}
		s := fmt.Sprintf("%s窗口: 剩余 %.0f%%", windowLabel(w.Label), *w.RemainingPct)
		if w.ResetAt != nil {
			s += "(" + humanDuration(time.Until(*w.ResetAt)) + "后重置)"
		}
		windows = append(windows, s)
	}
	detail := joinLines(windows)

	if level == "expiring" && expiring != nil {
		remaining := ""
		if expiring.RemainingCount != nil && expiring.TotalCount != nil {
			remaining = fmt.Sprintf("(约 %s/%s 次)", humanInt(*expiring.RemainingCount), humanInt(*expiring.TotalCount))
		}
		title := fmt.Sprintf("submon %s: %s配额即将重置", providerCN(name), windowLabel(expiring.Label))
		msg := fmt.Sprintf("%s窗口 %s 后重置,还有 %.0f%% 没用%s,抓紧使用!\n\n%s",
			windowLabel(expiring.Label), humanDuration(time.Until(*expiring.ResetAt)),
			*expiring.RemainingPct, remaining, detail)
		return title, msg
	}
	if level == "consumed" && expiring != nil {
		// 用得差不多了:静默级别,不发"抓紧用",只在状态记录里体现。
		return fmt.Sprintf("submon %s: %s窗口将重置(已充分使用)", providerCN(name), windowLabel(expiring.Label)), detail
	}
	// 回到 ok:窗口重置完成。
	return fmt.Sprintf("submon %s: 配额窗口已重置", providerCN(name)),
		"上一个窗口已重置,新窗口额度已生效。\n\n" + detail
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

var watchQuiet bool

func init() {
	watchCmd.Flags().BoolVar(&watchQuiet, "quiet", false, "静默模式(供 statusline 自愈调用)")
	rootCmd.AddCommand(watchCmd)
}
