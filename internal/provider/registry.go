package provider

import (
	"context"
	"os"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

// HTTPTimeout 是单个 provider 请求的超时上限 — 包含 DNS / TCP / TLS /
// 响应头 / 响应体。clig.deck robustness 要求"卡死要能跳出",不能无限等。
// 15s 兼容慢网络同时保证 watch 单轮不会拖太久。
const HTTPTimeout = 15 * time.Second

// DefaultZhipuBaseURL 是 zhipu 的内置默认基址(智谱开放平台)。
const DefaultZhipuBaseURL = "https://open.bigmodel.cn/api/anthropic"

// Build 构造 provider 默认账号的数据源(单账号老路径)。
// 凭据解析顺序:环境变量 > 默认账号 > 内置默认。
//
//	zhipu:    ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL > 默认账号 token/base_url > 内置默认基址
//	minimax:  MINIMAX_TOKEN > 默认账号 token
//	opencode: OPENCODE_API_KEY > 默认账号 token
//	kimi:     KIMI_CODING_API_KEY / KIMI_API_KEY > 默认账号 token > 内置默认基址
//
// 调用方需传入已解密的配置(Config.DecryptSecrets)。多账号场景请用 BuildAll。
func Build(name model.Provider, cfg *config.Config) (Source, error) {
	src, _, err := BuildOne(name, "", cfg)
	return src, err
}

// HasEnvCredential 报告 provider 的默认账号凭据是否由环境变量提供。
// env 名称与 BuildOne 的解析顺序保持一致 — check 体检用它区分
// "未配置"与"env 兜底",避免空 token 也被 Build 成功造成的假阳性。
func HasEnvCredential(name model.Provider) bool {
	switch name {
	case model.ProviderZhipu:
		return os.Getenv("ANTHROPIC_AUTH_TOKEN") != ""
	case model.ProviderMinimax:
		return os.Getenv("MINIMAX_TOKEN") != ""
	case model.ProviderOpenCode:
		return os.Getenv("OPENCODE_API_KEY") != ""
	case model.ProviderKimi:
		return os.Getenv("KIMI_CODING_API_KEY") != "" || os.Getenv("KIMI_API_KEY") != ""
	}
	return false
}

// BuildOne 给定 provider 与 label 构造一个 Source。Label 空或 "default"
// 时走默认账号(env 优先);其他 label 只在 [[accounts]] 里找同名账号。
//
// base_url 优先级保持老语义:默认账号 env > 账号 > 内置;命名账号
// 账号 > env > 内置。
func BuildOne(name model.Provider, label string, cfg *config.Config) (Source, string, error) {
	isDefault := label == "" || label == config.DefaultLabel
	acct := cfg.FindAccount(string(name), label)
	if !isDefault && acct == nil {
		return nil, "", &ErrNotConfigured{Reason: string(name) + " 无 key " + label}
	}
	// 命名账号在返回值里保留标签;默认账号统一归零(单 key 老路径)。
	outLabel := label
	if isDefault {
		outLabel = ""
	}

	switch name {
	case model.ProviderZhipu:
		baseURL := DefaultZhipuBaseURL
		if acct != nil && acct.BaseURL != "" {
			baseURL = acct.BaseURL
		}
		if isDefault || acct == nil || acct.BaseURL == "" {
			baseURL = firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), baseURL)
		}
		token := os.Getenv("ANTHROPIC_AUTH_TOKEN")
		if acct != nil && acct.Token != "" {
			if isDefault {
				token = firstNonEmpty(token, acct.Token)
			} else {
				token = acct.Token
			}
		}
		return &Zhipu{Token: token, BaseURL: originOf(baseURL)}, outLabel, nil
	case model.ProviderMinimax:
		token := os.Getenv("MINIMAX_TOKEN")
		if acct != nil && acct.Token != "" {
			if isDefault {
				token = firstNonEmpty(token, acct.Token)
			} else {
				token = acct.Token
			}
		}
		return &MiniMax{Token: token}, outLabel, nil
	case model.ProviderOpenCode:
		token := os.Getenv("OPENCODE_API_KEY")
		if acct != nil && acct.Token != "" {
			if isDefault {
				token = firstNonEmpty(token, acct.Token)
			} else {
				token = acct.Token
			}
		}
		return &OpenCode{Token: token}, outLabel, nil
	case model.ProviderKimi:
		baseURL := DefaultKimiBaseURL
		if acct != nil && acct.BaseURL != "" {
			baseURL = acct.BaseURL
		}
		token := firstNonEmpty(os.Getenv("KIMI_CODING_API_KEY"), os.Getenv("KIMI_API_KEY"))
		if acct != nil && acct.Token != "" {
			if isDefault {
				token = firstNonEmpty(token, acct.Token)
			} else {
				token = acct.Token
			}
		}
		return &Kimi{Token: token, BaseURL: baseURL}, outLabel, nil
	}
	return nil, "", &ErrNotConfigured{Reason: "未知 provider: " + string(name)}
}

// BuildAll 构造 provider 下所有已配置账号的数据源。返回 []LabeledSource,
// 每个元素都是一个独立 Source,持有自己的 KeyLabel 并在 Fetch 时把它
// 写入返回的 snapshot.KeyLabel。
//
// 顺序与 cfg.ProviderKeys 一致(文件内账号顺序,空 label 自动编号)。
func BuildAll(name model.Provider, cfg *config.Config) ([]LabeledSource, error) {
	keys := cfg.ProviderKeys(name)
	if len(keys) == 0 {
		// 至少 Build 一个,沿用 env 兜底(老用户没配 config 也想看)。
		src, _, err := BuildOne(name, "", cfg)
		if err != nil {
			return nil, err
		}
		// 没有 config 时仍视为 "default" 账号,这样面板里也能显示。
		return []LabeledSource{{Source: src, Label: "default"}}, nil
	}
	out := make([]LabeledSource, 0, len(keys))
	for _, k := range keys {
		src, _, err := BuildOne(name, k.Label, cfg)
		if err != nil {
			continue // 跳过配置错误的账号,不影响其他
		}
		out = append(out, LabeledSource{Source: src, Label: k.Label})
	}
	if len(out) == 0 {
		return nil, &ErrNotConfigured{Reason: string(name) + " 全部账号不可用"}
	}
	return out, nil
}

// LabeledSource 是 BuildAll 的产物 — 一个 Source 配一个 KeyLabel。
// 调用方调 Fetch 后 snapshot.KeyLabel 会被自动设置(LabeledSource
// 实现了 Source 接口,但额外接受自身 Label)。
type LabeledSource struct {
	Source
	Label string
}

// Fetch 转调底层 Source,然后把 Label 写入 snapshot。
func (l LabeledSource) Fetch(ctx context.Context) (*model.Snapshot, error) {
	snap, err := l.Source.Fetch(ctx)
	if err != nil {
		return nil, err
	}
	snap.KeyLabel = l.Label
	return snap, nil
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
