// Package cmd wires the submon cobra CLI.
package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "submon",
	Short: "Subscription monitor for Zhipu / MiniMax coding plans",
	Long: "submon monitors coding-plan quotas (Zhipu bigmodel.cn / z.ai, MiniMax)\n" +
		"with a shared cache so the statusline never hammers the APIs.",
	SilenceUsage:  true,
	SilenceErrors: true,
}

func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}
