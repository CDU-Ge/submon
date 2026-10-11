package routing

import (
	"github.com/ge/subscription-monitor/internal/model"
)

func opencodeRules() []Rule {
	return []Rule{
		{Provider: model.ProviderOpenCode, Hosts: []string{
			"*.opencode.ai", "opencode.ai",
			"*.opencode.com", "opencode.com",
		}},
	}
}
