package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/refresh"
	"github.com/spf13/cobra"
)

var (
	statusJSON  bool
	statusForce bool
)

var statusCmd = &cobra.Command{
	Use:   "status [provider]",
	Short: "查看订阅配额状态",
	Long: "展示配额面板。默认仅展示已配置的 provider(避免噪声):\n" +
		"想看某个特定 provider,显式写 `submon status <provider>`,未配置\n" +
		"的会显示原因。\n\n" +
		"provider: zhipu | minimax | opencode | kimi。省略则仅展示已配置的。\n" +
		"缓存未过期时直接读缓存;--force 强制刷新并更新缓存。\n\n" +
		"多 key 场景:同一 provider 配置了多个 [[keys]] 时,每个 key 单独\n" +
		"展示一段(标题里带 label)。",
	Example: "  submon status            # 仅展示已配置的 provider\n" +
		"  submon status zhipu      # 只看智谱(无论是否已配置)\n" +
		"  submon status --force    # 跳过缓存,重新拉全部 provider\n" +
		"  submon status --json     # 机器可读输出(供脚本 / jq)\n" +
		"  submon status --no-color # 关闭颜色(自动在管道时启用)",
	Args: cobra.MaximumNArgs(1),
	ValidArgs: func() []string {
		var names []string
		for _, p := range model.AllProviders() {
			names = append(names, string(p))
		}
		return names
	}(),
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := loadDecryptedConfig()
		if err != nil {
			return err
		}
		names, err := targetProviders(args, cfg)
		if err != nil {
			return err
		}

		path, err := cfg.CachePath()
		if err != nil {
			return err
		}
		store := cache.New(path)
		file, err := store.Load()
		if err != nil {
			return err
		}
		ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second
		ctx := context.Background()

		// 每个 (provider, label) 一条 row。多 key 场景:同一 provider
		// 多个 label 各占一条。
		type row struct {
			provider model.Provider
			label    string
			snap     *model.Snapshot
			err      error
			cached   bool
		}
		var rows []row
		for _, name := range names {
			keys := cfg.ProviderKeys(name)
			if len(keys) == 0 {
				// env 兜底:仍按单 key 旧语义显示,免得完全无反馈。
				e := file.Get(name)
				r := row{provider: name, label: "default"}
				if statusForce || !e.Fresh(ttl) {
					res := refresh.One(ctx, name, cfg, nil)
					r.snap, r.err = res.Snapshot, res.Err
				} else if e != nil && e.Snapshot != nil {
					r.snap = e.Snapshot
					r.cached = true
				} else if e != nil && e.LastErr != "" {
					r.err = fmt.Errorf("%s(缓存)", e.LastErr)
					r.cached = true
				}
				if r.snap == nil && r.err == nil {
					r.err = fmt.Errorf("获取失败")
				}
				rows = append(rows, r)
				continue
			}
			for _, k := range keys {
				e := file.GetLabeled(name, k.Label)
				r := row{provider: name, label: k.Label}
				if statusForce || !e.Fresh(ttl) {
					// 单 Key 刷新:走 BuildOne 而非 Many(后者会触发 N 次
					// HTTP — 这里每条 row 单独 fetch,与 Many 等价但
					// 失败粒度更细)。
					src, _, err := providerBuildOne(name, k.Label, cfg)
					if err != nil {
						r.err = err
					} else {
						snap, ferr := src.Fetch(ctx)
						if ferr != nil {
							r.err = ferr
						} else {
							snap.KeyLabel = k.Label
							r.snap = snap
						}
					}
				} else if e != nil && e.Snapshot != nil {
					r.snap = e.Snapshot
					r.cached = true
				} else if e != nil && e.LastErr != "" {
					r.err = fmt.Errorf("%s(缓存)", e.LastErr)
					r.cached = true
				}
				if r.snap == nil && r.err == nil {
					r.err = fmt.Errorf("无数据 — 先运行 `submon status --force` 获取")
				}
				rows = append(rows, r)
			}
		}

		if statusJSON {
			out := struct {
				Snapshots []*model.Snapshot `json:"snapshots"`
				Errors    []string          `json:"errors,omitempty"`
			}{}
			for _, r := range rows {
				if r.snap != nil {
					out.Snapshots = append(out.Snapshots, r.snap)
				}
				if r.err != nil {
					out.Errors = append(out.Errors, fmt.Sprintf("%s/%s: %s", r.provider, r.label, r.err))
				}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		failed := []string{}
		for i, r := range rows {
			if i > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if r.snap != nil {
				renderStatus(cmd.OutOrStdout(), r.snap, time.Now(), cfg)
				if r.err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "警告 %s/%s: %v\n", r.provider, r.label, r.err)
				}
				continue
			}
			failed = append(failed, fmt.Sprintf("%s/%s: %v", r.provider, r.label, r.err))
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s %s 不可用: %v\n",
				providerBadge(r.provider), r.label, ansiDim, r.err)
		}
		if len(failed) > 0 {
			fmt.Fprintln(cmd.ErrOrStderr(), "\n建议:")
			fmt.Fprintln(cmd.ErrOrStderr(), "  - token 失效:submon auth <provider> 重新配置")
			fmt.Fprintln(cmd.ErrOrStderr(), "  - 网络异常:重新运行 submon status --force")
			return fmt.Errorf("%d 项 provider 不可用 — 详见上方面板", len(failed))
		}
		return nil
	},
}

// targetProviders 解析目标 provider:显式参数 > 已配置的全部。
//
// 不传参时只返回已配置的(避免面板里出现 "kimi 未配置" 这类噪声 — 用户可能
// 只关心某个优先配额)。传了 provider 名则无脑返回它,未配置的情况交给
// RunE 在结果处显示原因。
func targetProviders(args []string, cfg *config.Config) ([]model.Provider, error) {
	if len(args) == 1 {
		name := model.Provider(args[0])
		if !model.ValidProvider(name) {
			return nil, fmt.Errorf("不认识 provider %q\n  可选: zhipu | minimax | opencode | kimi\n  看用法: submon status --help", args[0])
		}
		return []model.Provider{name}, nil
	}
	out := make([]model.Provider, 0, len(model.AllProviders()))
	for _, p := range model.AllProviders() {
		if providerConfigured(p, cfg) {
			out = append(out, p)
		}
	}
	return out, nil
}

// providerConfigured 判断 provider 是否至少有一个已配置 Key。
// 顶层 Token 或 [[keys]] 任一非空即算。这里 cfg 必须是 loadDecryptedConfig
// 的产物(encrypted 已展开),否则 enc:v1:... 会被误判为"已配置"。
func providerConfigured(p model.Provider, cfg *config.Config) bool {
	return len(cfg.ProviderKeys(p)) > 0
}

// providerBuildOne 是 provider.BuildOne 的薄封装 — 把 cmd 与 provider
// 包解耦,避免在 status.go 直接 import provider。
func providerBuildOne(name model.Provider, label string, cfg *config.Config) (labeledFetchable, string, error) {
	src, lbl, err := providerBuildByName(name, label, cfg)
	return src, lbl, err
}

// providerBuildByName 是 status.go → provider.BuildOne 的桥接变量,
// 测试时可替换成 stub(避免真实 HTTP)。默认调用 realProviderBuildOne。
var providerBuildByName func(name model.Provider, label string, cfg *config.Config) (labeledFetchable, string, error) = realProviderBuildOne

// labeledFetchable 是 status.go 与 provider.Source 之间的最小接口,
// 测试时可替换。
type labeledFetchable interface {
	Fetch(ctx context.Context) (*model.Snapshot, error)
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "机器可读输出")
	statusCmd.Flags().BoolVar(&statusForce, "force", false, "跳过缓存强制刷新")
	rootCmd.AddCommand(statusCmd)
}
