package routing

import (
	"github.com/ge/subscription-monitor/internal/model"
)

func minimaxRules() []Rule {
	return []Rule{
		{Provider: model.ProviderMinimax, Hosts: []string{
			"*.minimax.io", "minimax.io",
			"*.minimaxi.com", "minimaxi.com",
		}},
	}
}
