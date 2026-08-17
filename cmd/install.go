package cmd

import (
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
	installPrint  bool
	installUninst bool
	installTimer  string
)

const (
	unitName = "submon-watch"
	unitDesc = "submon one-shot refresh + quota alerts"
)

var installCmd = &cobra.Command{
	Use:   "install",
	Short: "安装 `submon watch` 的 systemd 用户定时器",
	Long: "生成并安装定期运行 `submon watch` 的 systemd 用户单元(service + timer),\n" +
		"然后启用 timer。\n\n" +
		"  submon install              安装并启用\n" +
		"  submon install --print      只预览生成的单元,不写入\n" +
		"  submon install --uninstall  停止、禁用并删除单元\n\n" +
		"环境变量凭据(ANTHROPIC_AUTH_TOKEN 等)不会写入 unit 文件 —\n" +
		"请放在 ~/.config/submon/config.toml(submon token set)\n" +
		"或 ~/.config/environment.d/ 中。",
	RunE: func(cmd *cobra.Command, args []string) error {
		if installUninst {
			return uninstallUnits(cmd)
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
		return nil
	},
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
	rootCmd.AddCommand(installCmd)
}
