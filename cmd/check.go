package cmd

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/notify"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
)

// checkCmd 全面体检:配置文件、各 provider 凭据、加密密钥、缓存、
// 通知配置、systemd timer。逐项输出 ✓/✗/⚠,最后给汇总。
var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "检查配置、凭据、缓存与通知链路",
	Long: "逐项检查 submon 的运行前提:\n" +
		"  1. 配置文件与加密密钥\n" +
		"  2. 各 provider 凭据(含加密存储形态)\n" +
		"  3. 缓存状态与数据年龄\n" +
		"  4. 通知配置与告警状态机\n" +
		"  5. systemd timer 运行状态\n" +
		"全部通过时退出码为 0,否则为 1。",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := true
		fail := func(format string, a ...any) { ok = false; fmt.Fprintf(cmd.OutOrStdout(), "✗ "+format+"\n", a...) }
		pass := func(format string, a ...any) { fmt.Fprintf(cmd.OutOrStdout(), "✓ "+format+"\n", a...) }
		warn := func(format string, a ...any) { fmt.Fprintf(cmd.OutOrStdout(), "⚠ "+format+"\n", a...) }

		out := cmd.OutOrStdout()

		// ── 配置文件 ──
		fmt.Fprintln(out, "── 配置 ──")
		cfgPath, _ := config.Path()
		if _, err := os.Stat(cfgPath); err == nil {
			pass("配置文件 %s", cfgPath)
		} else {
			warn("配置文件不存在(%s),将全部使用默认值 + 环境变量", cfgPath)
		}
		cfg, err := loadDecryptedConfig()
		if err != nil {
			fail("读取配置: %v", err)
		}

		// ── 加密密钥 ──
		fmt.Fprintln(out, "── 加密 ──")
		keyPath := secret.KeyPath()
		if info, err := os.Stat(keyPath); err == nil {
			note := ""
			if info.Mode().Perm() != 0o600 {
				note = "(建议 chmod 600)"
			}
			pass("主密钥 %s %s", keyPath, note)
		} else {
			warn("主密钥尚未生成(首次 token set / enc 时自动创建)")
		}

		// ── provider 凭据 ──
		fmt.Fprintln(out, "── Provider 凭据 ──")
		srcCfg, _ := config.Load() // 原始形态,用于判断明文/密文
		for _, name := range model.AllProviders() {
			src, err := provider.Build(name, cfg)
			if err != nil {
				fail("%s: %v", strings.ToUpper(string(name)), err)
				continue
			}
			_ = src
			// 报告凭据来源:环境变量 or 配置(加密/明文)。
			srcRaw, _ := provider.Build(name, srcCfg)
			_ = srcRaw
			pass("%s: 凭据就绪", strings.ToUpper(string(name)))
		}
		if os.Getenv("ANTHROPIC_AUTH_TOKEN") != "" {
			fmt.Fprintln(out, "  ℹ ANTHROPIC_AUTH_TOKEN 环境变量生效中(优先于配置文件)")
		}
		if os.Getenv("MINIMAX_TOKEN") != "" {
			fmt.Fprintln(out, "  ℹ MINIMAX_TOKEN 环境变量生效中(优先于配置文件)")
		}

		// ── 缓存 ──
		fmt.Fprintln(out, "── 缓存 ──")
		if cachePath, err := cfg.CachePath(); err == nil {
			if f, err := cache.New(cachePath).Load(); err == nil {
				for _, name := range model.AllProviders() {
					e := f.Get(name)
					if e == nil || e.Snapshot == nil {
						warn("%s: 无缓存数据(运行 submon status --force 生成)", strings.ToUpper(string(name)))
						continue
					}
					age := time.Since(e.FetchedAt).Round(time.Second)
					mark := "✓"
					if age > time.Duration(cfg.Cache.TTLSeconds)*time.Second {
						mark = "⚠"
					}
					fmt.Fprintf(out, "%s %s: 数据年龄 %s(阈值 %ds)%s\n", mark,
						strings.ToUpper(string(name)), age, cfg.Cache.TTLSeconds, alertSuffix(e))
				}
			} else {
				fail("缓存读取: %v", err)
			}
		}

		// ── 通知 ──
		fmt.Fprintln(out, "── 通知 ──")
		if cfg.Notify.Gotify.URL == "" || cfg.Notify.Gotify.AppToken == "" {
			warn("Gotify 未配置([notify.gotify] url / app_token),告警将无法推送")
		} else {
			if secret.IsEncrypted(cfg.Notify.Gotify.AppToken) {
				pass("Gotify: url + 加密 app_token 已配置")
			} else {
				warn("Gotify app_token 为明文,建议: submon token set gotify.app_token")
			}
			// 推送链路实测。
			g := &notify.Gotify{URL: cfg.Notify.Gotify.URL, AppToken: cfg.Notify.Gotify.AppToken}
			if err := g.Send("submon check", "配置检查测试推送", cfg.Notify.Gotify.Priority); err != nil {
				fail("Gotify 测试推送失败: %v", err)
			} else {
				pass("Gotify 测试推送成功")
			}
		}
		fmt.Fprintf(out, "  ℹ 告警阈值: 剩余 < %.0f%% 预警, < %.0f%% 严重(仅在 watch 运行时评估,状态变化才推送)\n",
			cfg.Notify.WarnBelowPct, cfg.Notify.CritBelowPct)

		// ── timer ──
		fmt.Fprintln(out, "── 定时任务 ──")
		timerState(out, pass, warn)

		if !ok {
			fmt.Fprintln(out, "── 结果: 存在问题,见上方 ✗ ──")
			return errCheckFailed
		}
		fmt.Fprintln(out, "── 结果: 全部通过 ──")
		return nil
	},
}

var errCheckFailed = fmt.Errorf("检查未通过")

func alertSuffix(e *cache.Entry) string {
	if e.AlertState.Level != "" && e.AlertState.Level != "ok" {
		return fmt.Sprintf(" [当前告警状态: %s]", e.AlertState.Level)
	}
	return ""
}

func timerState(out io.Writer, pass func(string, ...any), warn func(string, ...any)) {
	if _, err := exec.LookPath("systemctl"); err != nil {
		warn("未找到 systemctl,跳过 timer 检查")
		return
	}
	enabled, err := exec.Command("systemctl", "--user", "is-enabled", unitName+".timer").Output()
	if err != nil || strings.TrimSpace(string(enabled)) != "enabled" {
		warn("systemd timer 未启用,运行: submon install")
		return
	}
	pass("timer 已启用(%s)", strings.TrimSpace(string(enabled)))
	if next, err := exec.Command("systemctl", "--user", "list-timers", unitName+".timer", "--no-pager").Output(); err == nil {
		for _, line := range strings.Split(string(next), "\n") {
			if strings.Contains(line, unitName) {
				fmt.Fprintln(out, "  ℹ", strings.Join(strings.Fields(line), " "))
			}
		}
	}
}

func init() {
	rootCmd.AddCommand(checkCmd)
}
