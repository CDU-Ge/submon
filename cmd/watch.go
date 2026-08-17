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

// runAlertMachine 是"催用提醒"状态机:窗口临近重置时,按使用速率预测
// 重置时会浪费多少,决定是否提醒、以什么紧急度提醒:
//
//	urgent   预计浪费 ≥ waste_urgent_pct(60%)  → 🔴 紧急推送(priority+3)
//	notice   预计浪费 ≥ waste_notify_pct(30%)  → 普通推送
//	zero     完全未使用且临近重置(浪费 100% 的极端情形)→ 按普通推送,
//	         措辞区分"还没开始用",不吓人
//	usedup   预测显示会用完/用得差不多 → 不打扰
//	ok       离重置还远
//
// 仅在状态变化时推送;推送失败不推进状态(下个 tick 重试)。
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
	now := time.Now()

	newLevel := "ok"
	var hit *model.Window
	var wastePct *float64
	zeroUsage := false
	for _, w := range []*model.Window{snap.ShortWindow, snap.LongWindow} {
		if w == nil || w.ResetAt == nil || w.RemainingPct == nil {
			continue
		}
		if time.Until(*w.ResetAt) > cfg.RemindBeforeDuration() {
			continue
		}
		hit = w
		wp := w.PredictedWastePct(now)
		wastePct = wp
		switch {
		case wp == nil && w.UsedPct != nil && *w.UsedPct <= 0:
			// 极端值:窗口内零使用。剩余即全浪费,但措辞按"未使用"处理。
			zeroUsage = true
			newLevel = "notice"
		case wp != nil && *wp >= cfg.Notify.WasteUrgentPct:
			newLevel = "urgent"
		case wp != nil && *wp >= cfg.Notify.WasteNotifyPct:
			newLevel = "notice"
		default:
			newLevel = "usedup" // 预测会用完或浪费很少,不打扰
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

	title, msg, priority := formatAlert(name, snap, newLevel, hit, wastePct, zeroUsage, cfg)
	if title == "" {
		return nil // usedup:静默
	}
	if err := g.Send(title, msg, priority); err != nil {
		// 推送失败不推进状态,下个 tick 重试。
		log.Error("gotify 推送失败,状态不前进", "err", err)
		return err
	}
	e.AlertState = cache.AlertState{Level: newLevel, UpdatedAt: now, LastMessage: title + "\n" + msg}
	file.Set(name, e)
	if err := store.Save(file); err != nil {
		return err
	}
	log.Info("提醒状态变化", "provider", name, "from", prev, "to", newLevel)
	return nil
}

// formatAlert 生成催用通知。标题首词标明紧急度;正文给出预测依据。
// 返回 ("", "", 0) 表示该级别静默(usedup)。
func formatAlert(name model.Provider, snap *model.Snapshot, level string, hit *model.Window, wastePct *float64, zeroUsage bool, cfg *config.Config) (string, string, int) {
	if level == "usedup" {
		return "", "", 0
	}
	now := time.Now()
	detail := windowDetail(snap, now)

	prio := cfg.Notify.Gotify.Priority
	switch level {
	case "urgent":
		prio += 3
		if prio > 10 {
			prio = 10
		}
	case "notice":
		// 普通通知,默认优先级。
	}

	if hit == nil {
		// 回到 ok:窗口重置完成。
		return fmt.Sprintf("submon %s: 配额窗口已重置", providerCN(name)),
			"上一个窗口已重置,新窗口额度已生效。\n\n" + detail, prio
	}

	winCN := windowLabel(hit.Label)
	until := humanDuration(time.Until(*hit.ResetAt))
	leftPct := *hit.RemainingPct

	if zeroUsage {
		title := fmt.Sprintf("【提醒】%s %s配额还未使用,即将重置", providerCN(name), winCN)
		msg := fmt.Sprintf("%s配额 %s 后重置,目前剩余 %.0f%% 尚未使用。\n"+
			"如果这段时间不打算用可以忽略;要用就趁现在。\n\n%s",
			winCN, until, leftPct, detail)
		return title, msg, prio
	}

	wasteStr := "较多"
	if wastePct != nil {
		wasteStr = fmt.Sprintf("%.0f%%", *wastePct)
	}
	if level == "urgent" {
		title := fmt.Sprintf("【紧急】%s %s配额将浪费 %s", providerCN(name), winCN, wasteStr)
		msg := fmt.Sprintf("%s配额 %s 后重置,剩余 %.0f%%。\n"+
			"按当前使用速率,重置时预计还有 %s 额度用不完。\n"+
			"要用的活儿抓紧安排!\n\n%s",
			winCN, until, leftPct, wasteStr, detail)
		return title, msg, prio
	}
	title := fmt.Sprintf("【提醒】%s %s配额预计用不完(剩 %s)", providerCN(name), winCN, wasteStr)
	msg := fmt.Sprintf("%s配额 %s 后重置,剩余 %.0f%%,按当前速率预计浪费 %s。\n"+
		"有需要的话可以加把劲。\n\n%s",
		winCN, until, leftPct, wasteStr, detail)
	return title, msg, prio
}

// windowDetail 各窗口一行摘要(通知正文脚注)。
func windowDetail(snap *model.Snapshot, now time.Time) string {
	var lines []string
	for _, w := range []*model.Window{snap.ShortWindow, snap.LongWindow, snap.MonthlyToolWindow} {
		if w == nil || w.RemainingPct == nil {
			continue
		}
		s := fmt.Sprintf("%s窗口: 剩余 %.0f%%", windowLabel(w.Label), *w.RemainingPct)
		if w.ResetAt != nil {
			s += "(" + humanDuration(time.Until(*w.ResetAt)) + "后重置)"
		}
		lines = append(lines, s)
	}
	return joinLines(lines)
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
