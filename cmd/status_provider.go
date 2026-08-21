package cmd

import (
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/provider"
)

// realProviderBuildOne 是 providerBuildByName 的默认实现 — 把
// provider.BuildOne 收敛到一个最小返回值,让 status.go 不必 import
// provider(其它命令已经直接 import,这里是 status 自己的桥)。
func realProviderBuildOne(name model.Provider, label string, cfg *config.Config) (labeledFetchable, string, error) {
	src, lbl, err := provider.BuildOne(name, label, cfg)
	if err != nil {
		return nil, lbl, err
	}
	return src, lbl, nil
}
