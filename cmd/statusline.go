package cmd

import (
	"os"
	"time"

	"github.com/ge/subscription-monitor/internal/statusline"
	"github.com/spf13/cobra"
)

type cliTimingKey struct{}

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "渲染 Claude Code 状态栏",
	Long: "输出单行状态栏:左稳定(目录/git 分支/模型)→ 中关心(token 使用)\n" +
		"→ 右变动(effort/fast/订阅配额)。只读缓存,不联网。\n" +
		"provider 仅由 $ANTHROPIC_BASE_URL 自动识别。\n" +
		"都没有命中 → statusline 不显示配额段,不强行猜 provider。",
	Example: "  # 在 Claude Code settings.json 里:\n" +
		"  submon install              # 一键接入 ~/.claude/settings.json\n\n" +
		"  # 手动调用(不通过 CC):\n" +
		"  echo '{\"model\":{\"display_name\":\"GLM-5.3\"}}' | submon statusline\n" +
		"  submon statusline",
	RunE: func(cmd *cobra.Command, args []string) error {
		allowPipeColor = true
		refreshColorState()
		start, _ := cmd.Context().Value(cliTimingKey{}).(time.Time)
		return statusline.Run(os.Stdin, cmd.OutOrStdout(), cmd.ErrOrStderr(), statusline.Options{Color: useColor(), CLIStarted: start})
	},
}

func init() { rootCmd.AddCommand(statuslineCmd) }
