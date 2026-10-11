package routing

import (
	"github.com/ge/subscription-monitor/internal/model"
)

func kimiRules() []Rule {
	return []Rule{
		// Coding Plan 用 path 前缀窄化:任何 host 上挂着 /coding/v1 都视为 Kimi,
		// 这是用户私有代理场景里"推断 api 端点"的关键信号。
		{Provider: model.ProviderKimi, PathPrefix: "/coding/v1"},
		{Provider: model.ProviderKimi, Hosts: []string{
			"*.kimi.com", "kimi.com",
			"*.moonshot.cn", "moonshot.cn",
			"*.moonshot.com", "moonshot.com",
		}},
	}
}
