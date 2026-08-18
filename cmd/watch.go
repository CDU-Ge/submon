package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/notify"
	"github.com/ge/subscription-monitor/internal/predict"
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
			// 采样(供预测器估转换率),再评估。
			hist := recordSample(cfg, name, res.Snapshot)
			if err := runAlertMachine(ctx, cfg, g, name, res.Snapshot, hist, log); err != nil {
				log.Error("alert machine", "provider", name, "err", err)
			}
		}
		if failed {
			return fmt.Errorf("部分 provider 刷新失败")
		}
		return nil
	},
}

// recordSample 把本次快照的"已用%"写入历史,返回该 provider 的完整
// 历史(供预测器估转换率)。
func recordSample(cfg *config.Config, name model.Provider, snap *model.Snapshot) []cache.Sample {
	path, err := cfg.CachePath()
	if err != nil {
		return nil
	}
	store := cache.New(path)
	file, err := store.Load()
	if err != nil {
		return nil
	}
	s := cache.Sample{T: snap.FetchedAt}
	if snap.ShortWindow != nil && snap.ShortWindow.UsedPct != nil {
		s.ShortUsed = *snap.ShortWindow.UsedPct
	}
	if snap.LongWindow != nil && snap.LongWindow.UsedPct != nil {
		s.LongUsed = *snap.LongWindow.UsedPct
	}
	file.AppendSample(name, s, 0)
	if err := store.Save(file); err != nil {
		return nil
	}
	if e := file.Get(name); e != nil {
		return e.History
	}
	return nil
}

// runAlertMachine 是"催用提醒"状态机,两级评估:
//
//  1. weekly 命运(随时评估,不受 remind_before 约束):转换率 × 剩余可用
//     5h 窗口数(每日上限 3 个 — 人要休息)vs weekly 剩余:
//     inevitable-*  剩余 > 总消耗能力,无论如何都用不完 → 紧急/普通推送
//     (这种情况越早知道越好,提前几天就该推)
//
//  2. 5h 窗口(仅重置前 remind_before 内):速率外推:
//     urgent/notice/zero(未使用极端值)/usedup,阈值同前。
//
// 组合状态 = weekly级别:5h级别,任一变化即推送;推送失败不推进状态。
func runAlertMachine(ctx context.Context, cfg *config.Config, g *notify.Gotify, name model.Provider, snap *model.Snapshot, hist []cache.Sample, log *slog.Logger) error {
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

	// ── 第一级:weekly 必然浪费 ──
	fate := predict.WeeklyFate(snap.ShortWindow, snap.LongWindow, hist, now)
	weeklyLevel := "ok"
	if fate != nil && !fate.CanAvoid {
		switch {
		case fate.InevitablePct >= cfg.Notify.WasteUrgentPct:
			weeklyLevel = "inevitable-urgent"
		case fate.InevitablePct >= cfg.Notify.WasteNotifyPct:
			weeklyLevel = "inevitable-notice"
		}
	}

	// ── 第二级:5h 窗口临近重置的速率外推 ──
	shortLevel := "ok"
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
			zeroUsage = true
			shortLevel = "notice"
		case wp != nil && *wp >= cfg.Notify.WasteUrgentPct:
			shortLevel = "urgent"
		case wp != nil && *wp >= cfg.Notify.WasteNotifyPct:
			shortLevel = "notice"
		default:
			shortLevel = "usedup"
		}
		break
	}

	newState := weeklyLevel + ":" + shortLevel
	prev := e.AlertState.Level
	if prev == "" {
		prev = "ok:ok"
	}
	if newState == prev {
		return nil
	}

	title, msg, priority := formatAlert(name, snap, newState, weeklyLevel, fate, hit, wastePct, zeroUsage, cfg)
	if title == "" {
		// 静默级别:仍推进状态(避免每 tick 重复计算),不推送。
		e.AlertState = cache.AlertState{Level: newState, UpdatedAt: now}
		file.Set(name, e)
		_ = store.Save(file)
		return nil
	}
	if err := g.Send(title, msg, priority); err != nil {
		log.Error("gotify 推送失败,状态不前进", "err", err)
		return err
	}
	e.AlertState = cache.AlertState{Level: newState, UpdatedAt: now, LastMessage: title + "\n" + msg}
	file.Set(name, e)
	if err := store.Save(file); err != nil {
		return err
	}
	log.Info("提醒状态变化", "provider", name, "from", prev, "to", newState)
	return nil
}

// formatAlert 生成催用通知。优先报告 weekly 必然浪费(更严重、更早),
// 其次 5h 窗口预测。标题首词标紧急度。返回 ("", "", 0) 表示静默。
func formatAlert(name model.Provider, snap *model.Snapshot, state, weeklyLevel string, fate *predict.Result, hit *model.Window, wastePct *float64, zeroUsage bool, cfg *config.Config) (string, string, int) {
	now := time.Now()
	detail := windowDetail(snap, now)
	prio := cfg.Notify.Gotify.Priority
	urgentPrio := prio + 3
	if urgentPrio > 10 {
		urgentPrio = 10
	}

	// ── weekly 必然浪费(最高优先级叙事)──
	if strings.HasPrefix(weeklyLevel, "inevitable") && fate != nil {
		urgent := weeklyLevel == "inevitable-urgent"
		conv := fmt.Sprintf("每满载5h窗口约消耗 weekly 的 %.1f%%", fate.MaxBurnPerWindow)
		if fate.Conservative {
			conv += "(保守估计,历史样本不足)"
		}
		math := fmt.Sprintf(
			"重置前还能全力投入约 %.1f 个 5h 窗口(按每天最多 3 个,人要休息)\n"+
				"%s\n"+
				"总消耗能力 ≈ %.0f%% < 剩余 %.0f%%",
			fate.WindowsLeft, conv, fate.WindowsLeft*fate.MaxBurnPerWindow, *snap.LongWindow.RemainingPct)
		if urgent {
			title := fmt.Sprintf("【紧急】%s 每周配额注定浪费 %.0f%%", providerCN(name), fate.InevitablePct)
			msg := fmt.Sprintf("每周配额还剩 %.0f%%,但到重置前无论怎么用都用不完了:\n%s\n"+
				"预计浪费 %.0f%%。除非大幅提高使用强度,否则这部分就是白交钱。\n\n%s",
				*snap.LongWindow.RemainingPct, math, fate.InevitablePct, detail)
			return title, msg, urgentPrio
		}
		title := fmt.Sprintf("【提醒】%s 每周配额将浪费 %.0f%%", providerCN(name), fate.InevitablePct)
		msg := fmt.Sprintf("每周配额还剩 %.0f%%,按每天最多 3 个 5h 窗口算,重置前用不完:\n%s\n\n%s",
			*snap.LongWindow.RemainingPct, math, detail)
		return title, msg, prio
	}

	// ── 5h 窗口 ──
	if hit == nil {
		if state == "ok:ok" {
			return "", "", 0
		}
		// 回到 ok:窗口重置完成,轻描淡写。
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
	if strings.HasSuffix(state, "urgent") {
		title := fmt.Sprintf("【紧急】%s %s配额将浪费 %s", providerCN(name), winCN, wasteStr)
		msg := fmt.Sprintf("%s配额 %s 后重置,剩余 %.0f%%。\n"+
			"按当前使用速率,重置时预计还有 %s 额度用不完。\n"+
			"要用的活儿抓紧安排!\n\n%s",
			winCN, until, leftPct, wasteStr, detail)
		return title, msg, urgentPrio
	}
	if strings.HasSuffix(state, "notice") {
		title := fmt.Sprintf("【提醒】%s %s配额预计用不完(剩 %s)", providerCN(name), winCN, wasteStr)
		msg := fmt.Sprintf("%s配额 %s 后重置,剩余 %.0f%%,按当前速率预计浪费 %s。\n"+
			"有需要的话可以加把劲。\n\n%s",
			winCN, until, leftPct, wasteStr, detail)
		return title, msg, prio
	}
	// usedup / ok:静默。
	return "", "", 0
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
