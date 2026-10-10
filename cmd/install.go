package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

// installCmd wires submon into the system: registers the periodic `submon watch`
// job — a systemd user timer on POSIX, a Task Scheduler task on Windows, see
// schedule_*.go — and points the Claude Code statusline at this binary.
var (
	installPrint       bool
	installUninst      bool
	installTimer       string
	installNoClaude    bool // 跳过自动注册 CC 状态栏
	installClaudeOnly  bool // 只注册 CC 状态栏,跳过定时任务
	installClaudePrint bool // --print-claude 子标志:只预览 CC 状态栏要写入的内容
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "安装定时刷新与 Claude Code 状态栏",
	Long:  installLongText(),
	Example: "  submon install                 # 装定时任务 + 自动接 CC 状态栏\n" +
		"  submon install --no-claude     # 只装定时任务,不碰 Claude Code\n" +
		"  submon install --interval 1m   # 自定义触发间隔\n" +
		"  submon install --print         # 预览将写入的内容\n" +
		"  submon install --print-claude  # 预览 CC 状态栏 JSON,不读不写\n" +
		"  submon install --uninstall     # 停止 + 禁用 + 删除定时任务",
	RunE: func(cmd *cobra.Command, args []string) error {
		sched := newScheduler()
		if installUninst {
			return sched.Uninstall(cmd)
		}
		// --print-claude 优先:只预览 CC 状态栏,不读不写不装。
		if installClaudePrint {
			return runInstallClaude(cmd)
		}
		// --claude-only 模式:只做 CC 状态栏注册,跳过定时任务。
		if installClaudeOnly {
			return runInstallClaude(cmd)
		}
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		if bin, err = filepath.Abs(bin); err != nil {
			return err
		}
		if installPrint {
			return sched.Print(cmd, bin, installTimer)
		}
		if err := sched.Install(cmd, bin, installTimer); err != nil {
			return err
		}
		if !installNoClaude {
			if err := runInstallClaude(cmd); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "Claude Code 状态栏注册失败:", err)
			}
		}
		return nil
	},
}

// installLongText 的平台差异只有"定时任务由谁执行",其余流程两个平台一致 —
// 所以文案拼装放在函数里,而不是写死 systemd。
func installLongText() string {
	return "一站式安装:注册 `submon watch` 的定时任务(" + newScheduler().Name() + "),\n" +
		"**同时**自动检测 Claude Code 配置并把状态栏接上。\n\n" +
		"  submon install              安装定时任务 + 自动注册 CC 状态栏\n" +
		"  submon install --no-claude  只装定时任务,不碰 Claude Code\n" +
		"  submon install --claude-only 只注册 CC 状态栏,跳过定时任务\n" +
		"  submon install --print-claude 预览 CC 状态栏要写入的 JSON(不读不写)\n" +
		"  submon install --print      只预览将写入的内容,不落盘\n" +
		"  submon install --uninstall  停止、禁用并删除定时任务\n\n" +
		"子命令保留向后兼容:\n" +
		"  submon install claude       等同于 --claude-only(写 Claude Code 配置)\n\n" +
		"环境变量凭据(ANTHROPIC_AUTH_TOKEN 等)不会写进定时任务 —\n" +
		"请放在 " + configPathHint() + "(submon login)中。"
}

// runInstallClaude 把 statusLine 写入 Claude Code 的用户配置文件(保留其他
// 字段)。与 installClaudeCmd 共享同一份合并写入逻辑 — 用户即便之前没跑过
// `submon install claude`,主 install 路径也会自动跑一次。
//
// --print 模式:只渲染将写入的 JSON 片段,不读不写,适合想看看到底改了什么
// 再决定跑不跑。
func runInstallClaude(cmd *cobra.Command) error {
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
	settings := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("解析 %s: %w\n  手动修复 JSON 后重试,或 `mv %s %s.bak` 让 submon 重建", path, err, path, path)
		}
	}
	entry := map[string]any{
		"type":            "command",
		"command":         bin + " statusline",
		"padding":         0,
		"refreshInterval": 30,
	}
	if installClaudePrint {
		out, err := json.MarshalIndent(entry, "", "  ")
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "将写入", path, "的 statusLine 段:")
		fmt.Fprintln(cmd.OutOrStdout(), string(out))
		return nil
	}
	// 已经接好了就跳过 — 多次跑 install 不会反复覆盖用户改过的 statusLine 配置。
	if _, ok := settings["statusLine"]; ok {
		fmt.Fprintln(cmd.OutOrStdout(), "Claude Code 状态栏:已注册,跳过")
		return nil
	}
	settings["statusLine"] = entry
	out, err := json.MarshalIndent(settings, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Claude Code 状态栏:已写入", path, "(重启会话后生效)")
	return nil
}

// claudeSettingsPath 是 Claude Code 的用户级配置文件:~/.claude/settings.json
// (Windows 上是 %USERPROFILE%\.claude\settings.json)。
func claudeSettingsPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude", "settings.json"), nil
}

func init() {
	installCmd.Flags().BoolVar(&installPrint, "print", false, "只打印将写入的内容,不安装")
	installCmd.Flags().BoolVar(&installUninst, "uninstall", false, "停止、禁用并删除定时任务")
	installCmd.Flags().StringVar(&installTimer, "interval", "5m", "触发间隔(如 5m、15m、1h)")
	installCmd.Flags().BoolVar(&installNoClaude, "no-claude", false, "跳过自动注册 Claude Code 状态栏")
	installCmd.Flags().BoolVar(&installClaudeOnly, "claude-only", false, "只注册 Claude Code 状态栏,跳过定时任务")
	installCmd.PersistentFlags().BoolVar(&installClaudePrint, "print-claude", false, "(同 --claude-only --print) 只预览 CC 状态栏要写入的 JSON")
	rootCmd.AddCommand(installCmd)
}
