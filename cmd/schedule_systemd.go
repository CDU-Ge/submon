//go:build !windows

package cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// systemd 用户 timer 实现:一个 oneshot service(跑 submon watch)+ 一个
// timer 触发它。单元用真实二进制路径生成,本地编译的 binary 直接可用。

const (
	unitName = "submon-watch"
	unitDesc = "submon one-shot refresh + quota alerts"
)

type systemdScheduler struct{}

func newScheduler() scheduler { return systemdScheduler{} }

func (systemdScheduler) Name() string { return "systemd 用户 timer" }

func (systemdScheduler) Install(cmd *cobra.Command, bin, interval string) error {
	svc := renderUnit(serviceUnitTemplate, bin)
	tmr := strings.ReplaceAll(renderUnit(timerUnitTemplate, bin), "{INTERVAL}", interval)

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
	fmt.Fprintln(cmd.OutOrStdout(), "调度间隔:", interval)
	return nil
}

func (systemdScheduler) Uninstall(cmd *cobra.Command) error {
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

func (systemdScheduler) Print(cmd *cobra.Command, bin, interval string) error {
	svc := renderUnit(serviceUnitTemplate, bin)
	tmr := strings.ReplaceAll(renderUnit(timerUnitTemplate, bin), "{INTERVAL}", interval)
	fmt.Fprintf(cmd.OutOrStdout(), "--- %s.service ---\n%s", unitName, svc)
	fmt.Fprintf(cmd.OutOrStdout(), "--- %s.timer ---\n%s", unitName, tmr)
	return nil
}

// Status 走 systemctl --user is-enabled / list-timers。X11/Wayland 之外的
// 环境(比如 cron 里跑、没有 user session)拿不到状态,如实报 Note。
func (systemdScheduler) Status() scheduleStatus {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return scheduleStatus{Missing: true}
	}
	enabled, err := exec.Command("systemctl", "--user", "is-enabled", unitName+".timer").Output()
	state := strings.TrimSpace(string(enabled))
	if err != nil || state != "enabled" {
		note := state
		if note == "" {
			note = "未注册"
		}
		return scheduleStatus{Note: note}
	}
	st := scheduleStatus{Ready: true, Note: state}
	if next, err := exec.Command("systemctl", "--user", "list-timers", unitName+".timer", "--no-pager", "--no-legend").Output(); err == nil {
		// NEXT LEFT LAST PASSED UNIT ACTIVATES → 前两个字段就是下次触发时间。
		for _, line := range strings.Split(string(next), "\n") {
			fields := strings.Fields(line)
			if len(fields) >= 2 && strings.Contains(line, unitName) && fields[0] != "-" {
				st.NextRun = fields[0] + " " + fields[1]
				break
			}
		}
	}
	return st
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
