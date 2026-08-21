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

// Build 构造 provider 默认 Key 的数据源(单 key 老路径)。
// 凭据解析顺序:环境变量 > 配置文件 > 内置默认。
//
//	zhipu:    ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL > [zhipu] token/base_url > 默认基址
//	minimax:  MINIMAX_TOKEN > [minimax] token
//	opencode: OPENCODE_API_KEY > [opencode] token
//	kimi:     KIMI_CODING_API_KEY / KIMI_API_KEY > [kimi] token > 默认基址
//
// 调用方需传入已解密的配置(Config.DecryptSecrets)。多 key 场景请用 BuildAll。
func Build(name model.Provider, cfg *config.Config) (Source, error) {
	src, _, err := BuildOne(name, "", cfg)
	return src, err
}

// BuildOne 给定 provider 与 label 构造一个 Source。Label 非空时只在该
// label 下找 Token(查顶层默认 Key 或 [[keys]] 数组里对应 label);空时
// 取 env > 顶层 Token(单 key 老语义,不看 [[keys]] 数组 — 避免误把
// 命名 Key 也当默认返回)。
func BuildOne(name model.Provider, label string, cfg *config.Config) (Source, string, error) {
	switch name {
	case model.ProviderZhipu:
		baseURL := firstNonEmpty(os.Getenv("ANTHROPIC_BASE_URL"), cfg.Zhipu.BaseURL, DefaultZhipuBaseURL)
		token := firstNonEmpty(os.Getenv("ANTHROPIC_AUTH_TOKEN"), cfg.Zhipu.Token)
		if label == "default" {
			label = ""
		}
		if label != "" {
			k := findKey(cfg.Zhipu.Keys, label)
			if k == nil {
				return nil, "", &ErrNotConfigured{Reason: "zhipu 无 key " + label}
			}
			token = k.Token
			if k.BaseURL != "" {
				baseURL = k.BaseURL
			}
		}
		return &Zhipu{Token: token, BaseURL: originOf(baseURL)}, label, nil
	case model.ProviderMinimax:
		token := firstNonEmpty(os.Getenv("MINIMAX_TOKEN"), cfg.MiniMax.Token)
		if label == "default" {
			label = ""
		}
		if label != "" {
			k := findKey(cfg.MiniMax.Keys, label)
			if k == nil {
				return nil, "", &ErrNotConfigured{Reason: "minimax 无 key " + label}
			}
			token = k.Token
		}
		return &MiniMax{Token: token}, label, nil
	case model.ProviderOpenCode:
		token := firstNonEmpty(os.Getenv("OPENCODE_API_KEY"), cfg.OpenCode.Token)
		if label == "default" {
			label = ""
		}
		if label != "" {
			k := findKey(cfg.OpenCode.Keys, label)
			if k == nil {
				return nil, "", &ErrNotConfigured{Reason: "opencode 无 key " + label}
			}
			token = k.Token
		}
		return &OpenCode{Token: token}, label, nil
	case model.ProviderKimi:
		baseURL := firstNonEmpty(cfg.Kimi.BaseURL, DefaultKimiBaseURL)
		token := firstNonEmpty(os.Getenv("KIMI_CODING_API_KEY"), os.Getenv("KIMI_API_KEY"), cfg.Kimi.Token)
		if label == "default" {
			label = ""
		}
		if label != "" {
			k := findKey(cfg.Kimi.Keys, label)
			if k == nil {
				return nil, "", &ErrNotConfigured{Reason: "kimi 无 key " + label}
			}
			token = k.Token
			if k.BaseURL != "" {
				baseURL = k.BaseURL
			}
		}
		return &Kimi{Token: token, BaseURL: baseURL}, label, nil
	}
	return nil, "", &ErrNotConfigured{Reason: "未知 provider: " + string(name)}
}

// BuildAll 构造 provider 下所有已配置 Key 的数据源。返回 []LabeledSource,
// 每个元素都是一个独立 Source,持有自己的 KeyLabel 并在 Fetch 时把它
// 写入返回的 snapshot.KeyLabel。
//
// 顺序:顶层 default Token 在前(label = "default"),其后按 [[keys]] 数组
// 顺序。空 token / 空 label 自动编号已由 cfg.ProviderKeys 处理。
func BuildAll(name model.Provider, cfg *config.Config) ([]LabeledSource, error) {
	keys := cfg.ProviderKeys(name)
	if len(keys) == 0 {
		// 至少 Build 一个,沿用 env 兜底(老用户没配 config 也想看)。
		src, _, err := BuildOne(name, "", cfg)
		if err != nil {
			return nil, err
		}
		// 没有 config 时仍视为 "default" key,这样面板里也能显示。
		return []LabeledSource{{Source: src, Label: "default"}}, nil
	}
	out := make([]LabeledSource, 0, len(keys))
	for _, k := range keys {
		src, _, err := BuildOne(name, k.Label, cfg)
		if err != nil {
			continue // 跳过配置错误的 Key,不影响其他
		}
		out = append(out, LabeledSource{Source: src, Label: k.Label})
	}
	if len(out) == 0 {
		return nil, &ErrNotConfigured{Reason: string(name) + " 全部 key 不可用"}
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

// findKey 在 [[keys]] 数组里找 label 匹配的那条;找不到返回 nil。
func findKey(keys []config.ProviderKey, label string) *config.ProviderKey {
	for i := range keys {
		if keys[i].Label == label {
			return &keys[i]
		}
	}
	return nil
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
