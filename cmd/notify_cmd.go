package cmd

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/notify"
	"github.com/spf13/cobra"
)

var (
	notifyTest bool
	cacheClear bool
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "Interact with Gotify notifications",
	RunE: func(cmd *cobra.Command, args []string) error {
		if !notifyTest {
			return cmd.Usage()
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		g := &notify.Gotify{URL: cfg.Notify.Gotify.URL, AppToken: cfg.Notify.Gotify.AppToken}
		if err := g.Send("submon test", "Gotify connectivity OK", cfg.Notify.Gotify.Priority); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "notification sent")
		return nil
	},
}

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Inspect or clear the snapshot cache",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		path, err := cfg.CachePath()
		if err != nil {
			return err
		}
		if cacheClear {
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), "cache cleared:", path)
			return nil
		}
		f, err := cache.New(path).Load()
		if err != nil {
			return err
		}
		enc := json.NewEncoder(cmd.OutOrStdout())
		enc.SetIndent("", "  ")
		return enc.Encode(f)
	},
}

func init() {
	notifyCmd.Flags().BoolVar(&notifyTest, "test", false, "send a test push")
	cacheCmd.Flags().BoolVar(&cacheClear, "clear", false, "delete the cache file")
	rootCmd.AddCommand(notifyCmd, cacheCmd)
}
