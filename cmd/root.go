// Package cmd wires the submon cobra CLI.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "submon",
	Short: "Zhipu / MiniMax / OpenCode 订阅配额监控",
	Long: "submon 监控订阅配额(智谱 bigmodel.cn / z.ai、MiniMax、OpenCode),\n" +
		"共享缓存避免状态栏高频调用冲击 API 限额。",
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
