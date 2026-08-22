package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// installCmd wires submon into the system: installs the systemd user service
// + timer for `submon watch` and enables it. Units are generated with the
// real binary path so a locally built binary just works.
var (
	installPrint       bool
	installUninst      bool
	installTimer       string
	installNoClaude    bool // 跳过自动注册 CC 状态栏
	installClaudeOnly  bool // 只注册 CC 状态栏,跳过 systemd
	installClaudePrint bool // --print 子标志:只预览 CC 状态栏要写入的内容
)

const (
	unitName = "submon-watch"
	unitDesc = "submon one-shot refresh + quota alerts"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "安装 systemd 定时刷新与 Claude Code 状态栏",
	Long: "一站式安装:生成并启用 `submon watch` 的 systemd 用户定时器,\n" +
		"**同时**自动检测 ~/.claude/settings.json 并把状态栏接上。\n\n" +
		"  submon install              安装 timer + 自动注册 CC 状态栏\n" +
		"  submon install --no-claude  只装 timer,不碰 Claude Code\n" +
		"  submon install --claude-only 只注册 CC 状态栏,跳过 systemd\n" +
		"  submon install --print-claude 预览 CC 状态栏要写入的 JSON(不读不写)\n" +
		"  submon install --print      只预览生成的单元,不写入\n" +
		"  submon install --uninstall  停止、禁用并删除单元\n\n" +
		"子命令保留向后兼容:\n" +
		"  submon install claude       等同于 --claude-only(写 ~/.claude/settings.json)\n\n" +
		"环境变量凭据(ANTHROPIC_AUTH_TOKEN 等)不会写入 unit 文件 —\n" +
		"请放在 ~/.config/submon/config.toml(submon auth <provider>)\n" +
		"或 ~/.config/environment.d/ 中。",
	Example: "  submon install                 # 装 timer + 自动接 CC 状态栏\n" +
		"  submon install --no-claude     # 只装 systemd,不碰 Claude Code\n" +
		"  submon install --interval 1m   # 自定义触发间隔\n" +
		"  submon install --print         # 预览 unit 不写入\n" +
		"  submon install --print-claude  # 预览 CC 状态栏 JSON,不读不写\n" +
		"  submon install --uninstall     # 停止 + 禁用 + 删除单元",
	RunE: func(cmd *cobra.Command, args []string) error {
		if installUninst {
			return uninstallUnits(cmd)
		}
		// --print-claude 优先:只预览 CC 状态栏,不读不写不装。
		if installClaudePrint {
			return runInstallClaude(cmd)
		}
		// --claude-only 模式:只做 CC 状态栏注册,跳过 systemd。
		if installClaudeOnly {
			return runInstallClaude(cmd)
		}
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return err
		}
		svc := renderUnit(serviceUnitTemplate, bin)
		tmr := renderUnit(timerUnitTemplate, bin)
		tmr = strings.ReplaceAll(tmr, "{INTERVAL}", installTimer)

		if installPrint {
			fmt.Fprintf(cmd.OutOrStdout(), "--- %s.service ---\n%s", unitName, svc)
			fmt.Fprintf(cmd.OutOrStdout(), "--- %s.timer ---\n%s", unitName, tmr)
			return nil
		}

		dir := systemdUserDir()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		svcPath := filepath.Join(dir, unitName+".service")
		tmrPath := filepath.Join(dir, unitName+".timer")
		if err := os.WriteFile(svcPath, []byte(svc), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(tmrPath, []byte(tmr), 0o644); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已安装:", svcPath)
		fmt.Fprintln(cmd.OutOrStdout(), "已安装:", tmrPath)

		if !hasSystemdUser() {
			fmt.Fprintln(cmd.OutOrStderr(), "提示: 未检测到 systemd 用户会话,单元已写入但未启用")
			fmt.Fprintln(cmd.OutOrStderr(), "稍后手动启用: systemctl --user daemon-reload && systemctl --user enable --now", unitName+".timer")
			nudgeClaude(cmd)
			return nil
		}
		for _, args := range [][]string{
			{"daemon-reload"},
			{"enable", "--now", unitName + ".timer"},
		} {
			out, err := exec.Command("systemctl", append([]string{"--user"}, args...)...).CombinedOutput()
			if err != nil {
				return fmt.Errorf("systemctl --user %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
			}
		}
		fmt.Fprintln(cmd.OutOrStdout(), "timer 已启用:", unitName+".timer")
		fmt.Fprintln(cmd.OutOrStdout(), "调度间隔:", installTimer)
		if !installNoClaude {
			if err := runInstallClaude(cmd); err != nil {
				fmt.Fprintln(cmd.ErrOrStderr(), "Claude Code 状态栏注册失败:", err)
			}
		}
		return nil
	},
}

// runInstallClaude 把 statusLine 写入 ~/.claude/settings.json(保留其他字段)。
// 与 installClaudeCmd 共享同一份合并写入逻辑 — 用户即便之前没跑过
// `submon install claude`,主 install 路径也会自动跑一次。
//
// --print 模式:只渲染将写入 settings.json 的 JSON 片段,不读不写,适合
// 想看看到底改了什么再决定跑不跑。
func runInstallClaude(cmd *cobra.Command) error {
	bin, err := os.Executable()
	if err != nil {
		return err
	}
	bin, err = filepath.Abs(bin)
	if err != nil {
		return err
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return err
	}
	path := filepath.Join(home, ".claude", "settings.json")
	settings := map[string]any{}
	if raw, err := os.ReadFile(path); err == nil {
		if err := json.Unmarshal(raw, &settings); err != nil {
			return fmt.Errorf("解析 %s: %w\n  手动修复 JSON 后重试,或 `mv %s %s.bak` 让 submon 重建", path, err, path, path)
		}
	}
	if installClaudePrint {
		preview := map[string]any{
			"type":            "command",
			"command":         bin + " statusline",
			"padding":         0,
			"refreshInterval": 30,
		}
		out, err := json.MarshalIndent(preview, "", "  ")
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
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "Claude Code 状态栏:已写入", path, "(重启会话后生效)")
	return nil
}

// nudgeClaude 装完 systemd timer 后,如果用户显式 --no-claude 或已经接好
// CC 状态栏,什么都不做。仅留作 legacy — runInstallClaude 现在直接执行。
func nudgeClaude(cmd *cobra.Command) {
	home, err := os.UserHomeDir()
	if err != nil {
		return
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err == nil {
		var m map[string]any
		if json.Unmarshal(raw, &m) == nil {
			if _, ok := m["statusLine"]; ok {
				return
			}
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "")
	fmt.Fprintln(cmd.OutOrStdout(), "提示: Claude Code 状态栏未接入,运行 `submon install claude` 完成注册")
}

func uninstallUnits(cmd *cobra.Command) error {
	dir := systemdUserDir()
	if hasSystemdUser() {
		_ = exec.Command("systemctl", "--user", "disable", "--now", unitName+".timer").Run()
		_ = exec.Command("systemctl", "--user", "daemon-reload").Run()
	}
	for _, f := range []string{unitName + ".service", unitName + ".timer"} {
		path := filepath.Join(dir, f)
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已删除单元:", dir)
	return nil
}

func systemdUserDir() string {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "systemd", "user")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", "systemd", "user")
}

func hasSystemdUser() bool {
	if os.Getenv("XDG_RUNTIME_DIR") == "" {
		return false
	}
	return exec.Command("systemctl", "--user", "status").Run() == nil
}

func renderUnit(tmpl, bin string) string {
	return strings.ReplaceAll(tmpl, "{BIN}", bin)
}

// Unit templates: credentials intentionally left to config.toml /
// environment.d — never baked into unit files.
const serviceUnitTemplate = `[Unit]
Description=` + unitDesc + `

[Service]
Type=oneshot
ExecStart={BIN} watch
`

const timerUnitTemplate = `[Unit]
Description=Run submon watch every 5 minutes

[Timer]
OnBootSec=2min
OnUnitActiveSec={INTERVAL}
AccuracySec=30s

[Install]
WantedBy=timers.target
`

func init() {
	installCmd.Flags().BoolVar(&installPrint, "print", false, "只打印生成的单元,不安装")
	installCmd.Flags().BoolVar(&installUninst, "uninstall", false, "停止、禁用并删除单元")
	installCmd.Flags().StringVar(&installTimer, "interval", "5m", "触发间隔(systemd 时长格式,如 5m、15m)")
	installCmd.Flags().BoolVar(&installNoClaude, "no-claude", false, "跳过自动注册 Claude Code 状态栏")
	installCmd.Flags().BoolVar(&installClaudeOnly, "claude-only", false, "只注册 Claude Code 状态栏,跳过 systemd")
	installCmd.PersistentFlags().BoolVar(&installClaudePrint, "print-claude", false, "(同 --claude-only --print) 只预览 CC 状态栏要写入的 JSON")
	rootCmd.AddCommand(installCmd)
}
