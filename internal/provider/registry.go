package provider

import (
	"os"
	"strings"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

// DefaultZhipuBaseURL 是 zhipu 的内置默认基址(智谱开放平台)。
const DefaultZhipuBaseURL = "https://open.bigmodel.cn/api/anthropic"

// Build 构造 provider 数据源。凭据解析顺序:环境变量 > 配置文件 > 内置默认。
//
//	zhipu:   ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL > [zhipu] token/base_url > 默认基址
//	minimax: MINIMAX_TOKEN > [minimax] token
//
// 调用方需传入已解密的配置(Config.DecryptSecrets)。
func Build(name model.Provider, cfg *config.Config) (Source, error) {
	switch name {
	case model.ProviderZhipu:
		baseURL := firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), cfg.Zhipu.BaseURL, DefaultZhipuBaseURL)
		token := firstNonEmpty(os.Getenv("ANTHROPIC_AUTH_TOKEN"), cfg.Zhipu.Token)
		// 监控接口挂在源站根路径,不在 /api/anthropic 之下。
		return &Zhipu{Token: token, BaseURL: originOf(baseURL)}, nil
	case model.ProviderMinimax:
		token := firstNonEmpty(os.Getenv("MINIMAX_TOKEN"), cfg.MiniMax.Token)
		return &MiniMax{Token: token}, nil
	}
	return nil, &ErrNotConfigured{Reason: "未知 provider: " + string(name)}
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
}

// originOf 去掉基址的路径部分("https://open.bigmodel.cn/api/anthropic"
// → "https://open.bigmodel.cn")。
func originOf(baseURL string) string {
	if i := strings.Index(baseURL, "://"); i >= 0 {
		rest := baseURL[i+3:]
		if j := strings.IndexAny(rest, "/"); j >= 0 {
			return baseURL[:i+3+j]
		}
	}
	return baseURL
}
