package cmd

import (
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/spf13/cobra"
)

// notifyStatusCmd 展示催用提醒状态机的当前状态,回答"什么时候会通知我":
//   - 各窗口距离重置还有多久、剩余多少
//   - 进入"催用提醒窗口"(remind_before 内 + 剩余 ≥ use_above)的预计时间
//   - watch 何时会再运行
var notifyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看催用提醒状态与下次评估时间",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadDecryptedConfig()
		if err != nil {
			return err
		}
		out := cmd.OutOrStdout()
		path, err := cfg.CachePath()
		if err != nil {
			return err
		}
		file, err := cache.New(path).Load()
		if err != nil {
			return err
		}

		remind := cfg.RemindBeforeDuration()
		useAbove := cfg.Notify.UseAbovePct
		if remind == 0 {
			fmt.Fprintln(out, "催用提醒已关闭(notify.remind_before = 0)")
		} else {
			fmt.Fprintf(out, "规则: 窗口将在 %s 内重置 且 剩余 ≥ %.0f%% → 推送「抓紧使用」\n",
				humanDuration(remind), useAbove)
			fmt.Fprintln(out, "      (剩余很少 = 已用够本,不打扰;窗口重置后回到静默)")
			fmt.Fprintln(out)
		}

		any := false
		for _, name := range []model.Provider{model.ProviderZhipu, model.ProviderMinimax} {
			e := file.Get(name)
			if e == nil || e.Snapshot == nil {
				continue
			}
			any = true
			fmt.Fprintf(out, "%s:\n", strings.ToUpper(string(name)))
			for _, w := range []*model.Window{e.Snapshot.ShortWindow, e.Snapshot.LongWindow} {
				if w == nil || w.ResetAt == nil {
					continue
				}
				until := time.Until(*w.ResetAt)
				mark := " "
				note := ""
				if w.RemainingPct != nil {
					if until <= remind && *w.RemainingPct >= useAbove {
						mark, note = "⚠", " ← 在提醒窗口内,下次 watch 将推送"
					} else if until <= remind {
						mark, note = "·", " (剩余不多,不催)"
					} else if remind > 0 {
						note = fmt.Sprintf("(距提醒窗口还有 %s)", humanDuration(until-remind))
					}
				}
				pct := "n/a"
				if w.RemainingPct != nil {
					pct = fmt.Sprintf("%.0f%%", *w.RemainingPct)
				}
				fmt.Fprintf(out, "%s  %s窗口: 剩余 %s · %s 后重置%s\n",
					mark, windowLabel(w.Label), pct, humanDuration(until), note)
			}
			if e.AlertState.LastMessage != "" {
				fmt.Fprintf(out, "  最后推送: %s\n", firstLine(e.AlertState.LastMessage))
			}
		}
		if !any {
			fmt.Fprintln(out, "(暂无数据,先运行 submon status --force)")
		}

		// systemd timer 下次触发时间。
		fmt.Fprintln(out)
		if raw, err := exec.Command("systemctl", "--user", "list-timers", unitName+".timer", "--no-pager", "--no-legend").Output(); err == nil {
			for _, line := range strings.Split(string(raw), "\n") {
				fields := strings.Fields(line)
				// NEXT LEFT LAST PASSED UNIT ACTIVATES → 下次时间为第三个字段后…
				// 格式含日期+时间,取 "NEXT" 列(前两个字段:日期 时间)。
				if len(fields) >= 3 && strings.Contains(line, unitName) && fields[0] != "-" {
					fmt.Fprintf(out, "下次评估: %s %s %s(由 %s.timer 触发)\n", fields[0], fields[1], fields[2], unitName)
					return nil
				}
			}
		}
		fmt.Fprintln(out, "下次评估: 未知(timer 未启用?运行 submon install)")
		return nil
	},
}

var notifyArmCmd = &cobra.Command{
	Use:   "arm",
	Short: "重置告警状态(用于测试下一次推送)",
	Long: "把所有 provider 的告警状态重置为 ok。\n" +
		"下次 watch 运行时,若配额确实低于阈值,会重新推送。\n" +
		"主要用于调试通知链路。",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		path, err := cfg.CachePath()
		if err != nil {
			return err
		}
		store := cache.New(path)
		file, err := store.Load()
		if err != nil {
			return err
		}
		for _, name := range []model.Provider{model.ProviderZhipu, model.ProviderMinimax} {
			if e := file.Get(name); e != nil {
				e.AlertState = cache.AlertState{Level: "ok", UpdatedAt: time.Now()}
			}
		}
		if err := store.Save(file); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "告警状态已重置为 ok")
		return nil
	},
}

func init() {
	notifyCmd.AddCommand(notifyStatusCmd, notifyArmCmd, notifyTestCmd)
}

// firstLine 截取首行(LastMessage 是多行正文)。
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
