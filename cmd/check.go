package cmd

import (
	"fmt"
	"io"
	"os"
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
// 通知配置、定时任务。逐项输出 ✓/✗/⚠,最后给汇总。
var checkCmd = &cobra.Command{
	Use:   "check",
	Short: "检查配置、凭据、缓存与通知链路",
	Long: "逐项检查 submon 的运行前提:\n" +
		"  1. 配置文件与加密密钥\n" +
		"  2. 各 provider 凭据(含加密存储形态)\n" +
		"  3. 缓存状态与数据年龄\n" +
		"  4. 通知配置与告警状态机\n" +
		"  5. 定时任务(systemd timer / 任务计划程序)运行状态\n" +
		"全部通过时退出码为 0,否则为 1。",
	Example: "  submon check             # 全项体检\n" +
		"  submon check && echo ok  # 流水线里用退出码判断",
	RunE: func(cmd *cobra.Command, args []string) error {
		ok := true
		var passN, warnN, failN int
		fail := func(format string, a ...any) {
			ok = false
			failN++
			fmt.Fprintf(cmd.OutOrStdout(), "✗ "+format+"\n", a...)
		}
		pass := func(format string, a ...any) {
			passN++
			fmt.Fprintf(cmd.OutOrStdout(), "✓ "+format+"\n", a...)
		}
		warn := func(format string, a ...any) {
			warnN++
			fmt.Fprintf(cmd.OutOrStdout(), "⚠ "+format+"\n", a...)
		}

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
		if cfg == nil {
			// 解析失败时退化为默认配置,让后续检查逐项报,而不是 panic。
			cfg = config.Default()
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
			warn("主密钥尚未生成(首次 submon login / enc 时自动创建)")
		}

		// ── provider 凭据 ──
		fmt.Fprintln(out, "── Provider 凭据 ──")
		srcCfg, err := config.Load() // 原始形态,用于判断明文/密文
		if err != nil || srcCfg == nil {
			srcCfg = config.Default()
		}
		for _, name := range model.AllProviders() {
			disp := strings.ToUpper(string(name))
			accounts := srcCfg.ProviderKeys(name)
			if len(accounts) == 0 {
				// Build 对空 token 也构造成功 — 已配置与否要看 env / 配置真值,
				// 否则全新环境会得到"凭据就绪"的假阳性。
				if provider.HasEnvCredential(name) {
					pass("%s: 凭据就绪(环境变量)", disp)
				} else {
					warn("%s: 未配置(运行 submon login)", disp)
				}
				continue
			}
			var problems []string
			for _, a := range accounts {
				switch {
				case !secret.IsEncrypted(a.Token):
					problems = append(problems, a.Label+" 存在明文(重新运行 submon login)")
				default:
					if _, err := secret.Decrypt(a.Token); err != nil {
						problems = append(problems, a.Label+" 解密失败("+err.Error()+")")
					}
				}
			}
			if len(problems) == 0 {
				pass("%s: 凭据就绪(%d 个账号,加密存储)", disp, len(accounts))
			} else {
				fail("%s: %s", disp, strings.Join(problems, "; "))
			}
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
		gURL, gTokenRaw := srcCfg.GotifyCredential()
		_, gToken := cfg.GotifyCredential()
		if gURL == "" || gTokenRaw == "" {
			warn("Gotify 未配置([[accounts]] 中 provider = \"gotify\"),告警将无法推送")
		} else {
			if secret.IsEncrypted(gTokenRaw) {
				pass("Gotify: url + 加密 app_token 已配置")
			} else {
				warn("Gotify app_token 为明文,建议重新运行: submon login")
			}
			// 推送链路实测。
			g := &notify.Gotify{URL: gURL, AppToken: gToken}
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
			fmt.Fprintf(out, "── 结果: %d 项失败 / %d 项待处理 — 见上方 ✗ ──\n", failN, warnN)
			return errCheckFailed
		}
		if warnN > 0 {
			fmt.Fprintf(out, "── 结果: 无阻断问题(%d 项通过,%d 项建议处理)— 见上方 ⚠ ──\n", passN, warnN)
		} else {
			fmt.Fprintf(out, "── 结果: 全部通过(%d 项)──\n", passN)
		}
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

// timerState 检查"定时刷新"是否已注册并启用。调度机制按平台不同
// (systemd timer / 任务计划程序),这里只消费 scheduler 抽象。
func timerState(out io.Writer, pass func(string, ...any), warn func(string, ...any)) {
	sched := newScheduler()
	st := sched.Status()
	switch {
	case st.Missing:
		warn("未找到 %s,跳过定时任务检查", sched.Name())
	case !st.Ready:
		warn("%s 未启用(%s),运行: submon install", sched.Name(), st.Note)
	default:
		pass("%s 已启用(%s)", sched.Name(), st.Note)
		if st.NextRun != "" {
			fmt.Fprintln(out, "  ℹ 下次触发:", st.NextRun)
		}
	}
}

func init() {
	rootCmd.AddCommand(checkCmd)
}
