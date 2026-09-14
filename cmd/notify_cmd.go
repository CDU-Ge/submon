package cmd

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/notify"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

var notifyCmd = &cobra.Command{
	Use:   "notify",
	Short: "管理通知推送与告警状态",
	Example: "  submon notify test     # 发一条测试推送(验 Gotify 链路)\n" +
		"  submon notify status   # 看当前告警状态机\n" +
		"  submon notify reset    # 重置状态机,允许下次 tick 重新评估",
	RunE: func(cmd *cobra.Command, args []string) error {
		// 无子命令时输出帮助 — cobra 默认是 print usage 但不退出 0,
		// 这里返回 nil 让 cobra 自动打印 Usage,符合"无参运行显示帮助"。
		return cmd.Help()
	},
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
		gURL, gToken := cfg.GotifyCredential()
		g := &notify.Gotify{URL: gURL, AppToken: gToken}
		if err := g.Send("submon 测试", "通知链路正常", cfg.Notify.Gotify.Priority); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已发送测试推送")
		return nil
	},
}

// cacheCmd 是 submon 缓存的查看与清理入口。
//
//	cache             → 列出缓存(provider + label + 年龄 + 告警状态)
//	cache clear       → 删除缓存文件(默认要求 y/N 确认)
//	cache clear -f    → 强制清理(脚本友好,跳过确认)
//
// 与早期版本的差异:之前是 `cache --clear` flag — flag 是"修饰主命令"的
// 语义(像 ls --color);清空是另一个动作,放子命令更直白。`cache` 无子
// 命令时 = `cache ls`,back-compat 友好。
var cacheClearForce bool
var cacheListJSON bool

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "查看或清空快照缓存",
	Long: "submon 的快照缓存(~/.cache/submon/cache.json)为 statusline 提供\n" +
		"零成本读访问,watch 定时器负责写入:\n\n" +
		"  submon cache             列出全部 provider 的缓存条目\n" +
		"  submon cache clear       删除缓存文件(要求 y/N 确认)\n" +
		"  submon cache clear -f    强制清理(脚本友好)",
	Example: "  submon cache             # 列出当前所有缓存条目\n" +
		"  submon cache clear       # 交互式确认删除\n" +
		"  submon cache clear -f    # 脚本里强制清",
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 1 && args[0] == "clear" {
			return runCacheClear(cmd)
		}
		return runCacheList(cmd)
	},
}

func init() {
	cacheCmd.Flags().BoolVarP(&cacheClearForce, "force", "f", false, "强制清空缓存(跳过 y/N 确认;脚本里用)")
	cacheCmd.Flags().BoolVar(&cacheListJSON, "json", false, "机器可读输出(JSON)")
	rootCmd.AddCommand(notifyCmd, cacheCmd)
}

// runCacheList 默认输出人读表格(provider/label、数据年龄、告警状态);
// --json 输出原始 JSON(脚本 / jq)。
func runCacheList(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	path, err := cfg.CachePath()
	if err != nil {
		return err
	}
	f, err := cache.New(path).Load()
	if err != nil {
		return err
	}
	out := cmd.OutOrStdout()
	if cacheListJSON {
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(f)
	}

	keys := make([]string, 0, len(f.Entries))
	for k, e := range f.Entries {
		if e != nil {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	if len(keys) == 0 {
		fmt.Fprintln(out, "(缓存为空 — 运行 submon status --force 或 submon watch 生成)")
		return nil
	}
	for _, k := range keys {
		e := f.Entries[k]
		line := fmt.Sprintf("%-16s", k)
		switch {
		case e.Snapshot != nil:
			line += fmt.Sprintf("%s 前更新", humanDuration(time.Since(e.FetchedAt).Round(time.Second)))
			if lvl := e.AlertState.Level; lvl != "" && lvl != "ok" && lvl != "ok:ok" {
				line += " · 告警状态 " + lvl
			}
		case e.LastErr != "":
			line += "无快照 · 上次错误: " + firstLine(e.LastErr)
		default:
			line += "空条目"
		}
		fmt.Fprintln(out, line)
	}
	return nil
}

// runCacheClear 删除缓存文件。属于"中度危险"动作 — 缓存删除后下次
// watch 会重新拉数据,但用户在排查问题时会丢失现场。规则:
//
//   - --force:直接删(脚本友好)
//   - TTY:提示 y/N 确认
//   - 非 TTY 且无 --force:报错并提示重试方式(避免脚本误删)
func runCacheClear(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	path, err := cfg.CachePath()
	if err != nil {
		return err
	}

	if !cacheClearForce {
		// TTY:提示确认;非 TTY:拒绝执行(避免 CI 误删现场)。
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf(
				"非交互式环境拒绝执行 cache clear(避免脚本误删):\n" +
					"  强制清理: submon cache clear -f\n" +
					"  或在 TTY 里直接跑并回答 y")
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "将删除缓存文件 %s — 确认继续? [y/N]: ", path)
		br := bufio.NewReader(os.Stdin)
		line, err := br.ReadString('\n')
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(line), "y") {
			fmt.Fprintln(cmd.OutOrStdout(), "已取消")
			return nil
		}
	}

	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("删除缓存 %s: %w", path, err)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "缓存已清理:", path)
	return nil
}
