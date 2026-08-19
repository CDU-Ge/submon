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
		"缓存未过期时直接读缓存;--force 强制刷新并更新缓存。",
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

		type row struct {
			snap *model.Snapshot
			err  error
		}
		var rows []row
		for _, name := range names {
			e := file.Get(name)
			if statusForce || !e.Fresh(ttl) {
				res := refresh.One(ctx, name, cfg, nil)
				r := row{snap: res.Snapshot, err: res.Err}
				if r.snap == nil && r.err == nil {
					r.err = fmt.Errorf("获取失败")
				}
				rows = append(rows, r)
				continue
			}
			r := row{}
			if e.Snapshot != nil {
				r.snap = e.Snapshot
			} else if e.LastErr != "" {
				r.err = fmt.Errorf("%s(缓存)", e.LastErr)
			}
			rows = append(rows, r)
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
					out.Errors = append(out.Errors, r.err.Error())
				}
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		failed := false
		for i, r := range rows {
			if i > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			if r.snap != nil {
				renderStatus(cmd.OutOrStdout(), r.snap, time.Now(), cfg)
				if r.err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "警告: %v\n", r.err)
				}
				continue
			}
			failed = true
			fmt.Fprintf(cmd.OutOrStdout(), "%s %s 未配置或不可用: %v\n",
				providerBadge(names[i]), ansiDim, r.err)
		}
		if failed {
			return fmt.Errorf("部分 provider 不可用")
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
			return nil, fmt.Errorf("未知 provider %q(zhipu|minimax|opencode|kimi)", args[0])
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

// providerConfigured 判断 provider 是否已配 token。BaseURL 是可选的,
// 不参与。这里 cfg 必须是 loadDecryptedConfig 的产物(encrypted 已展开),
// 否则 enc:v1:... 会被误判为"已配置"。
func providerConfigured(p model.Provider, cfg *config.Config) bool {
	switch p {
	case model.ProviderZhipu:
		return cfg.Zhipu.Token != ""
	case model.ProviderMinimax:
		return cfg.MiniMax.Token != ""
	case model.ProviderOpenCode:
		return cfg.OpenCode.Token != ""
	case model.ProviderKimi:
		return cfg.Kimi.Token != ""
	}
	return false
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "机器可读输出")
	statusCmd.Flags().BoolVar(&statusForce, "force", false, "跳过缓存强制刷新")
	rootCmd.AddCommand(statusCmd)
}
