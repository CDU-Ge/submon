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
	Long: "展示配额面板。默认展示全部 provider:\n" +
		"有凭据的展示数据,缺凭据的标注原因。\n\n" +
		"provider: zhipu | minimax | opencode。省略则展示全部。\n" +
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

// targetProviders 解析目标 provider:显式参数 > 全部。
// 未配置的也会在输出中标注原因,不再静默跳过。
func targetProviders(args []string, cfg *config.Config) ([]model.Provider, error) {
	if len(args) == 1 {
		name := model.Provider(args[0])
		if !model.ValidProvider(name) {
			return nil, fmt.Errorf("未知 provider %q(zhipu|minimax|opencode)", args[0])
		}
		return []model.Provider{name}, nil
	}
	return model.AllProviders(), nil
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "机器可读输出")
	statusCmd.Flags().BoolVar(&statusForce, "force", false, "跳过缓存强制刷新")
	rootCmd.AddCommand(statusCmd)
}
