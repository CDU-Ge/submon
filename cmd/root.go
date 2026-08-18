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
		"共享缓存保证 statusline 高频调用不打爆 API。",
	Version:       "dev",
	SilenceUsage:  true,
	SilenceErrors: true,
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
