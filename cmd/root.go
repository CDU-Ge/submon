// Package cmd wires the submon cobra CLI.
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "submon",
	Short: "Subscription monitor for Zhipu / MiniMax coding plans",
	Long: "submon monitors coding-plan quotas (Zhipu bigmodel.cn / z.ai, MiniMax)\n" +
		"with a shared cache so the statusline never hammers the APIs.",
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
