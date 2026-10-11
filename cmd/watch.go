package cmd

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
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

// watch is a one-shot job meant to be launched by the platform scheduler
// (systemd user timer / Windows 任务计划程序,见 `submon install`) every few
// minutes: force-refresh every configured provider, update the cache
// (which also feeds statusline), run the alert state machine and push Gotify
// notifications on level *transitions* only, then exit.
var watchCmd = &cobra.Command{
	Use:   "watch",
	Short: "刷新配额缓存并评估催用提醒",
	Long: "刷新全部 provider、更新共享缓存;窗口即将重置且剩余额度较多时\n" +
		"推送催用提醒。设计为每次 timer 触发运行一次。",
	Example: "  submon watch                  # 正常模式(供定时任务)\n" +
		"  submon watch --quiet          # 静默模式\n" +
		"  submon install                # 一键注册定时任务(systemd timer / 任务计划程序)",
	RunE: func(cmd *cobra.Command, args []string) error {
		if watchQuiet {
			// 静默模式:日志全部丢弃。
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

		gURL, gToken := cfg.GotifyCredential()
		g := &notify.Gotify{URL: gURL, AppToken: gToken}
		// Ctrl-C / SIGTERM(termSignals,Windows 只有 Ctrl-C):第一次按 =
		// 取消 in-flight HTTP,清干净退出;
		// 第二次按 = 立即放弃,不写缓存。两段 ctx:用户取消 + 60s 总预算,
		// 任何一个先到都让所有 provider Fetch 收到 Done 信号。
		ctx, stopSig := signal.NotifyContext(context.Background(), termSignals()...)
		defer stopSig()
		ctx, cancel := context.WithTimeout(ctx, 60*time.Second)
		defer cancel()
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, termSignals()...)
		go func() {
			<-sigCh
			if !watchQuiet {
				fmt.Fprintln(cmd.ErrOrStderr(), "\n收到中断信号 — 取消 in-flight 刷新,正在清理... (再按一次强退)")
			}
			cancel()
			<-sigCh
			if !watchQuiet {
				fmt.Fprintln(cmd.ErrOrStderr(), "\n再次收到中断 — 强制退出,不写缓存")
			}
			os.Exit(130)
		}()
		defer signal.Stop(sigCh)

		var failed []string
		for _, name := range names {
			// 多 key 场景:每个 Key 独立取一次 — provider 通常按账号
			// 独立计数配额,合并 fetch 既不准确也覆盖不到。
			results := refresh.Many(ctx, name, cfg, log)
			for _, res := range results {
				if res.Err != nil {
					label := res.KeyLabel
					if label == "" {
						label = "default"
					}
					failed = append(failed, fmt.Sprintf("%s/%s: %v", name, label, res.Err))
					continue
				}
				// 采样(供预测器估转换率),再评估。
				hist := recordSample(cfg, name, res.KeyLabel, res.Snapshot)
				if err := runAlertMachine(ctx, cfg, g, name, res.KeyLabel, res.Snapshot, hist, log); err != nil {
					log.Error("alert machine", "provider", name, "label", res.KeyLabel, "err", err)
				}
			}
		}
		if len(failed) > 0 {
			// 汇总到 stderr 让人眼能扫到每条具体 provider 失败原因,
			// 再用一个简洁的 error 让定时任务 / shell 拿到非零退出码。
			for _, line := range failed {
				fmt.Fprintln(cmd.ErrOrStderr(), "✗", line)
			}
			return fmt.Errorf("%d 项刷新失败 — 详见上方 ✗ 行(网络/token 过期/限额都会触发)", len(failed))
		}
		return nil
	},
}

// recordSample 把本次快照的"已用%"写入历史,返回该 (provider, label)
// 的完整历史(供预测器估转换率)。多 key 场景:label 区分,每个 Key
// 各自一份 sample 序列。
func recordSample(cfg *config.Config, name model.Provider, label string, snap *model.Snapshot) []cache.Sample {
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
	if e := file.GetLabeled(name, label); e != nil {
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
//
// label 区分多 key 各自的告警状态机;空 label 走单 key 老路径。
func runAlertMachine(ctx context.Context, cfg *config.Config, g *notify.Gotify, name model.Provider, label string, snap *model.Snapshot, hist []cache.Sample, log *slog.Logger) error {
	path, err := cfg.CachePath()
	if err != nil {
		return err
	}
	store := cache.New(path)
	file, err := store.Load()
	if err != nil {
		return err
	}
	e := file.GetLabeled(name, label)
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

	title, msg, priority := formatAlert(name, label, snap, newState, weeklyLevel, fate, hit, wastePct, zeroUsage, cfg)
	if title == "" {
		// 静默级别:仍推进状态(避免每 tick 重复计算),不推送。
		e.AlertState = cache.AlertState{Level: newState, UpdatedAt: now}
		file.SetLabeled(name, label, e)
		_ = store.Save(file)
		return nil
	}
	if err := g.Send(title, msg, priority); err != nil {
		log.Error("gotify 推送失败,状态不前进", "err", err)
		return err
	}
	e.AlertState = cache.AlertState{Level: newState, UpdatedAt: now, LastMessage: title + "\n" + msg}
	file.SetLabeled(name, label, e)
	if err := store.Save(file); err != nil {
		return err
	}
	log.Info("提醒状态变化", "provider", name, "label", label, "from", prev, "to", newState)
	return nil
}

// formatAlert 生成催用通知。优先报告 weekly 必然浪费(更严重、更早),
// 其次 5h 窗口预测。标题首词标紧急度。返回 ("", "", 0) 表示静默。
// label 非空且非 default 时拼到 provider 中文名后(如"智谱/alice")。
func formatAlert(name model.Provider, label string, snap *model.Snapshot, state, weeklyLevel string, fate *predict.Result, hit *model.Window, wastePct *float64, zeroUsage bool, cfg *config.Config) (string, string, int) {
	now := time.Now()
	detail := windowDetail(snap, now)
	provName := providerCN(name)
	if label != "" && label != "default" {
		provName += "/" + label
	}
	prio := cfg.Notify.Gotify.Priority
	urgentPrio := prio + 3
	if urgentPrio > 10 {
		urgentPrio = 10
	}

	// ── weekly 必然浪费(最高优先级叙事)──
	if strings.HasPrefix(weeklyLevel, "inevitable") && fate != nil {
		urgent := weeklyLevel == "inevitable-urgent"
		conv := fmt.Sprintf("每个满载 5h 窗口约消耗 weekly 的 %.1f%%", fate.MaxBurnPerWindow)
		if fate.Conservative {
			conv += "(保守估计,历史样本不足)"
		}
		math := fmt.Sprintf(
			"重置前剩余可用约 %.1f 个 5h 窗口(按每日上限 3 个窗口估算)\n"+
				"%s\n"+
				"总消耗能力 ≈ %.0f%% < 剩余 %.0f%%",
			fate.WindowsLeft, conv, fate.WindowsLeft*fate.MaxBurnPerWindow, *snap.LongWindow.RemainingPct)
		if urgent {
			title := fmt.Sprintf("【紧急】%s 每周配额预计浪费 %.0f%%", provName, fate.InevitablePct)
			msg := fmt.Sprintf("每周配额剩余 %.0f%%,重置前消耗能力低于剩余额度:\n%s\n"+
				"预计 %.0f%% 额度未使用即过期。\n\n%s",
				*snap.LongWindow.RemainingPct, math, fate.InevitablePct, detail)
			return title, msg, urgentPrio
		}
		title := fmt.Sprintf("【提醒】%s 每周配额将浪费 %.0f%%", provName, fate.InevitablePct)
		msg := fmt.Sprintf("每周配额剩余 %.0f%%,按每日上限 3 个 5h 窗口估算,重置前无法用完:\n%s\n\n%s",
			*snap.LongWindow.RemainingPct, math, detail)
		return title, msg, prio
	}

	// ── 5h 窗口 ──
	if hit == nil {
		if state == "ok:ok" {
			return "", "", 0
		}
		// 回到 ok:窗口重置完成,轻描淡写。
		return fmt.Sprintf("submon %s: 配额窗口已重置", provName),
			"上一个窗口已重置,新窗口额度已生效。\n\n" + detail, prio
	}

	winCN := windowLabel(hit.Label)
	until := humanDuration(time.Until(*hit.ResetAt))
	leftPct := *hit.RemainingPct

	if zeroUsage {
		title := fmt.Sprintf("【提醒】%s %s配额未使用,即将重置", provName, winCN)
		msg := fmt.Sprintf("%s窗口将于 %s 后重置,当前剩余 %.0f%% 未使用。\n"+
			"若无使用计划可忽略本提醒。\n\n%s",
			winCN, until, leftPct, detail)
		return title, msg, prio
	}

	wasteStr := "较多"
	if wastePct != nil {
		wasteStr = fmt.Sprintf("%.0f%%", *wastePct)
	}
	if strings.HasSuffix(state, "urgent") {
		title := fmt.Sprintf("【紧急】%s %s配额将浪费 %s", provName, winCN, wasteStr)
		msg := fmt.Sprintf("%s窗口将于 %s 后重置,剩余 %.0f%%。\n"+
			"按当前使用速率,重置时预计 %s 额度未消耗。\n\n%s",
			winCN, until, leftPct, wasteStr, detail)
		return title, msg, urgentPrio
	}
	if strings.HasSuffix(state, "notice") {
		title := fmt.Sprintf("【提醒】%s %s配额预计用不完(剩 %s)", provName, winCN, wasteStr)
		msg := fmt.Sprintf("%s窗口将于 %s 后重置,剩余 %.0f%%,按当前速率预计浪费 %s。\n"+
			"如有需要可在重置前提高使用强度。\n\n%s",
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

// providerCN 中文 provider 名。
func providerCN(name model.Provider) string {
	switch name {
	case model.ProviderZhipu:
		return "智谱"
	case model.ProviderOpenCode:
		return "OpenCode"
	case model.ProviderKimi:
		return "Kimi"
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
	watchCmd.Flags().BoolVar(&watchQuiet, "quiet", false, "静默模式")
	rootCmd.AddCommand(watchCmd)
}
