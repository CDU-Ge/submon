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
	cacheClear bool
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "通知管理(测试推送 / 查看告警状态)",
}

// notifyTestCmd 发送测试推送,验证 Gotify 链路。
var notifyTestCmd = &cobra.Command{
	Use:   "test",
	Short: "发送一条测试推送",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadDecryptedConfig()
		if err != nil {
			return err
		}
		g := &notify.Gotify{URL: cfg.Notify.Gotify.URL, AppToken: cfg.Notify.Gotify.AppToken}
		if err := g.Send("submon 测试", "通知链路正常", cfg.Notify.Gotify.Priority); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已发送测试推送")
		return nil
	},
}

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "查看或清空快照缓存",
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
	cacheCmd.Flags().BoolVar(&cacheClear, "clear", false, "删除缓存文件")
	rootCmd.AddCommand(notifyCmd, cacheCmd)
}
