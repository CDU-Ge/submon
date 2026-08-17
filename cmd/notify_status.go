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

// notifyStatusCmd 展示告警状态机的当前状态,回答"什么时候会通知我":
//   - 每个 provider 的当前级别(ok/warn/crit)与最低剩余百分比
//   - 距离下一个阈值还差多少
//   - watch 何时会再运行(systemd timer 下次触发时间)
var notifyStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "查看告警状态与下次评估时间",
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

		warnAt, critAt := cfg.Notify.WarnBelowPct, cfg.Notify.CritBelowPct
		fmt.Fprintf(out, "阈值: 剩余 <%.0f%% → 预警(warn), <%.0f%% → 严重(crit)\n", warnAt, critAt)
		fmt.Fprintf(out, "规则: 仅 watch 运行时评估;状态发生变化才推送,持平不推送\n\n")

		any := false
		for _, name := range []model.Provider{model.ProviderZhipu, model.ProviderMinimax} {
			e := file.Get(name)
			if e == nil || e.Snapshot == nil {
				continue
			}
			any = true
			level := e.AlertState.Level
			if level == "" {
				level = "ok"
			}
			lowest := e.Snapshot.CriticalPct()
			pctStr, nextStr := "n/a", ""
			if lowest != nil {
				pctStr = fmt.Sprintf("%.0f%%", *lowest)
				switch {
				case *lowest < critAt:
					nextStr = "已低于严重阈值,回升后将推送「已恢复」"
				case *lowest < warnAt:
					nextStr = fmt.Sprintf("再降 %.0f%% 到严重阈值(<%.0f%%)", *lowest-critAt, critAt)
				default:
					nextStr = fmt.Sprintf("再降 %.0f%% 到预警阈值(<%.0f%%)", *lowest-warnAt, warnAt)
				}
			}
			icon := map[string]string{"ok": "✓", "warn": "⚠", "crit": "✗"}[level]
			fmt.Fprintf(out, "%s %s: 级别 %s · 最低剩余 %s · %s\n",
				icon, strings.ToUpper(string(name)), level, pctStr, nextStr)
			if e.AlertState.LastMessage != "" {
				fmt.Fprintf(out, "  最后一次推送: %s\n", strings.TrimSpace(e.AlertState.LastMessage))
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
