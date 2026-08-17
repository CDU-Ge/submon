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
	Short: "Install the systemd user timer for `submon watch`",
	Long: "Generate and install the systemd user units (service + timer) that run\n" +
		"`submon watch` on a schedule, then enable the timer.\n\n" +
		"  submon install            install + enable\n" +
		"  submon install --print    show generated units without writing\n" +
		"  submon install --uninstall  stop + disable + remove units\n\n" +
		"Environment credentials (ANTHROPIC_AUTH_TOKEN etc.) are NOT copied into\n" +
		"the unit — put them in ~/.config/submon/config.toml (submon token set)\n" +
		"or drop a file in ~/.config/environment.d/ instead.",
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
		fmt.Fprintln(cmd.OutOrStdout(), "installed:", svcPath)
		fmt.Fprintln(cmd.OutOrStdout(), "installed:", tmrPath)

		if !hasSystemdUser() {
			fmt.Fprintln(cmd.OutOrStderr(), "note: systemd user session not detected; units written but not enabled")
			fmt.Fprintln(cmd.OutOrStderr(), "enable later with: systemctl --user daemon-reload && systemctl --user enable --now", unitName+".timer")
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
		fmt.Fprintln(cmd.OutOrStdout(), "timer enabled:", unitName+".timer")
		fmt.Fprintln(cmd.OutOrStdout(), "next runs scheduled every", installTimer)
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
	fmt.Fprintln(cmd.OutOrStdout(), "removed units from", dir)
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
	installCmd.Flags().BoolVar(&installPrint, "print", false, "print generated units without installing")
	installCmd.Flags().BoolVar(&installUninst, "uninstall", false, "stop, disable and remove the units")
	installCmd.Flags().StringVar(&installTimer, "interval", "5m", "timer interval (systemd duration, e.g. 5m, 15m)")
	rootCmd.AddCommand(installCmd)
}
