package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// installClaudeCmd 自动配置 Claude Code 的 statusLine:合并写入
// ~/.claude/settings.json(保留其他字段),statusLine 指向当前二进制的
// 绝对路径,refreshInterval 采用默认最优值 30(空闲时倒计时也保持新鲜,
// statusline 只读缓存,30s 重跑一次零成本)。
var installClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "把 Claude Code 状态栏指向 submon",
	Long: "把 Claude Code 的 statusLine 指向 submon:\n\n" +
		"  submon install claude          写入 ~/.claude/settings.json\n" +
		"  submon install claude --print  只预览将写入的 JSON\n\n" +
		"已有配置合并保留(不覆盖其他字段);refreshInterval=30 让空闲会话\n" +
		"的状态栏倒计时也保持新鲜 —— statusline 只读缓存,高频调用无额外开销。",
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return err
		}

		path, err := claudeSettingsPath()
		if err != nil {
			return err
		}

		// 合并:读现有 JSON(任意未知字段原样保留),只动 statusLine。
		settings := map[string]any{}
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &settings); err != nil {
				return fmt.Errorf("解析 %s: %w(手动修复后重试)", path, err)
			}
		}
		settings["statusLine"] = map[string]any{
			"type":            "command",
			"command":         bin + " statusline",
			"padding":         0,
			"refreshInterval": 30,
		}
		out, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}

		if claudePrint {
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已写入:", path)
		fmt.Fprintln(cmd.OutOrStdout(), "重启 Claude Code 会话后生效")
		return nil
	},
}

var claudePrint bool

func init() {
	installClaudeCmd.Flags().BoolVar(&claudePrint, "print", false, "只预览将写入的 JSON,不落盘")
	installCmd.AddCommand(installClaudeCmd)
}
