package provider

import (
	"os"
	"strings"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Build returns the source for a provider name, wiring credentials from
// config + environment:
//
//	zhipu:   cfg.Zhipu.Token       or ANTHROPIC_AUTH_TOKEN
//	         cfg.Zhipu.BaseURL     or ANTHROPIC_BASE_URL
//	minimax: cfg.MiniMax.Token    or ~/.minimax-config.json
func Build(name model.Provider, cfg *config.Config) (Source, error) {
	switch name {
	case model.ProviderZhipu:
		z := &Zhipu{Token: cfg.Zhipu.Token, BaseURL: cfg.Zhipu.BaseURL}
		if z.Token == "" {
			z.Token = envOr("ANTHROPIC_AUTH_TOKEN", "")
		}
		if z.BaseURL == "" {
			z.BaseURL = envOr("ANTHROPIC_BASE_URL", "")
		}
		if z.BaseURL == "" {
			return nil, &ErrNotConfigured{Reason: "zhipu: no base URL (set ANTHROPIC_BASE_URL or config zhipu.base_url)"}
		}
		// The monitor endpoints live on the origin, not under /api/anthropic.
		z.BaseURL = originOf(z.BaseURL)
		return z, nil
	case model.ProviderMinimax:
		return &MiniMax{Token: cfg.MiniMax.Token}, nil
	}
	return nil, &ErrNotConfigured{Reason: "unknown provider: " + string(name)}
}

// originOf strips the path from a base URL ("https://open.bigmodel.cn/api/anthropic"
// → "https://open.bigmodel.cn").
func originOf(baseURL string) string {
	if i := strings.Index(baseURL, "://"); i >= 0 {
		rest := baseURL[i+3:]
		if j := strings.IndexAny(rest, "/"); j >= 0 {
			return baseURL[:i+3+j]
		}
	}
	return baseURL
}
