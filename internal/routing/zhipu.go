package routing

import (
	"github.com/ge/subscription-monitor/internal/model"
)

func zhipuRules() []Rule {
	return []Rule{
		// 智谱 bigmodel.cn (open / dev),裸 host + 子域通吃。
		{Provider: model.ProviderZhipu, Hosts: []string{
			"*.bigmodel.cn", "bigmodel.cn",
		}},
		// Z.ai 的 anthropic 兼容端点 — 单独一条 + path 前缀,把它的
		// anthropic-compatible API 与其他 Z.ai 服务区分开。
		{Provider: model.ProviderZhipu, Hosts: []string{
			"*.z.ai", "z.ai",
		}, PathPrefix: "/api/anthropic"},
		// Z.ai 的其余接口也按 zhipu 走(同一计费账户)。
		{Provider: model.ProviderZhipu, Hosts: []string{
			"*.z.ai", "z.ai",
		}},
	}
}
