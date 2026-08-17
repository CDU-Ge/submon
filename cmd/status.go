package cmd

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/ge/subscription-monitor/internal/refresh"
	"github.com/spf13/cobra"
)

var (
	statusJSON  bool
	statusForce bool
)

var statusCmd = &cobra.Command{
	Use:   "status [provider]",
	Short: "Show subscription quota status",
	Long: "Show quota status for one or all configured providers.\n" +
		"Reads from cache when fresh; --force refetches and updates the cache.\n\n" +
		"Providers: zhipu, minimax. Omit to query all configured providers.",
	Args: cobra.MaximumNArgs(1),
	ValidArgs: []string{
		string(model.ProviderZhipu),
		string(model.ProviderMinimax),
	},
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

		var snapshots []*model.Snapshot
		var errs []error
		for _, name := range names {
			e := file.Get(name)
			if statusForce || !e.Fresh(ttl) {
				res := refresh.One(ctx, name, cfg, nil)
				if res.Err != nil {
					errs = append(errs, fmt.Errorf("%s: %w", name, res.Err))
				}
				if res.Snapshot != nil {
					snapshots = append(snapshots, res.Snapshot)
				}
				continue
			}
			if e.Snapshot != nil {
				snapshots = append(snapshots, e.Snapshot)
			} else if e.LastErr != "" {
				errs = append(errs, fmt.Errorf("%s: %s (cached)", name, e.LastErr))
			}
		}

		if statusJSON {
			out := struct {
				Snapshots []*model.Snapshot `json:"snapshots"`
				Errors    []string          `json:"errors,omitempty"`
			}{Snapshots: snapshots}
			for _, e := range errs {
				out.Errors = append(out.Errors, e.Error())
			}
			enc := json.NewEncoder(cmd.OutOrStdout())
			enc.SetIndent("", "  ")
			return enc.Encode(out)
		}

		if len(snapshots) == 0 && len(errs) > 0 {
			for _, e := range errs {
				fmt.Fprintln(cmd.ErrOrStderr(), "error:", e)
			}
			return fmt.Errorf("no snapshot available")
		}
		for i, s := range snapshots {
			if i > 0 {
				fmt.Fprintln(cmd.OutOrStdout())
			}
			renderStatus(cmd.OutOrStdout(), s, time.Now())
		}
		for _, e := range errs {
			fmt.Fprintln(cmd.ErrOrStderr(), "warning:", e)
		}
		return nil
	},
}

// targetProviders resolves which providers to act on: explicit arg > all with
// resolvable credentials.
func targetProviders(args []string, cfg *config.Config) ([]model.Provider, error) {
	if len(args) == 1 {
		name := model.Provider(args[0])
		if name != model.ProviderZhipu && name != model.ProviderMinimax {
			return nil, fmt.Errorf("unknown provider %q (zhipu|minimax)", args[0])
		}
		return []model.Provider{name}, nil
	}
	var names []model.Provider
	for _, name := range []model.Provider{model.ProviderZhipu, model.ProviderMinimax} {
		if _, err := provider.Build(name, cfg); err == nil {
			names = append(names, name)
		}
	}
	if len(names) == 0 {
		return nil, fmt.Errorf("no provider configured: set ANTHROPIC_BASE_URL/ANTHROPIC_AUTH_TOKEN for zhipu, or minimax token")
	}
	return names, nil
}

func init() {
	statusCmd.Flags().BoolVar(&statusJSON, "json", false, "machine-readable output")
	statusCmd.Flags().BoolVar(&statusForce, "force", false, "bypass cache and refetch")
	rootCmd.AddCommand(statusCmd)
}
