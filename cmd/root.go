// Package cmd wires the submon cobra CLI.
package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/ge/subscription-monitor/internal/model"
	"github.com/spf13/cobra"
)

// providerListHint 从 AllProviders() 生成 "zhipu | minimax | ..." 提示,
// 避免帮助文案里的硬编码清单与 provider 清单漂移。
func providerListHint() string {
	names := make([]string, 0, len(model.AllProviders()))
	for _, p := range model.AllProviders() {
		names = append(names, string(p))
	}
	return strings.Join(names, " | ")
}

var rootCmd = &cobra.Command{
	Use:   "submon",
	Short: "订阅配额监控(智谱 / MiniMax / OpenCode / Kimi)",
	Long: "submon 监控订阅配额(智谱 bigmodel.cn / z.ai、MiniMax、OpenCode、Kimi),\n" +
		"共享缓存避免状态栏高频调用冲击 API 限额。\n\n" +
		"上手:\n" +
		"  submon auth zhipu     配置凭据(其他可选: " + providerListHint() + ")\n" +
		"  submon install        安装定时刷新与 Claude Code 状态栏\n" +
		"  submon status         查看配额面板",
	Version:       "dev",
	SilenceUsage:  true,
	SilenceErrors: true,
	// PersistentPreRun 在任何子命令 RunE 之前跑一次 — 此时 cobra 已
	// 解析完 argv(--no-color flag 已确定),根据 useColor() 刷新 ansi* 变量。
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		refreshColorState()
		return nil
	},
}

// versionCmd 显式版本查询 — 一些用户本能打 `submon version` 而不是 --version
// flag。cobra 自带 --version,但 sub-command 给一个明确的入口也好。
var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "显示版本信息",
	Example: "  submon version           # 打印当前版本\n" +
		"  submon version | grep    # 流水线里 grep 版本号",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Fprintln(cmd.OutOrStdout(), "submon", rootCmd.Version)
	},
}

// SetVersion wires build-time version info (called from main).
func SetVersion(version, commit string) {
	rootCmd.Version = version + " (" + commit + ")"
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
