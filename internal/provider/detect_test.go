package provider

import (
	"errors"
	"strings"
	"testing"

	"github.com/ge/subscription-monitor/internal/model"
)

// TestDetectFromBaseURL 表驱动覆盖:
//   - 每个 provider 的 host(精确 + *. 后缀)
//   - 大小写不敏感
//   - 端口 + 尾部斜杠 + path 前缀
//   - 用户自定义规则覆盖内建
//   - priority 决定 tie-break
//   - 未知 host 返回 ErrUnknownProvider 并带 host / path 诊断
func TestDetectFromBaseURL(t *testing.T) {
	cases := []struct {
		name    string
		url     string
		extras  []Rule
		want    model.Provider
		wantErr bool
	}{
		// --- 内建规则:精确 host ---
		{"zhipu-bare", "https://open.bigmodel.cn/api/anthropic", nil, model.ProviderZhipu, false},
		{"zhipu-zai", "https://api.z.ai/api/anthropic", nil, model.ProviderZhipu, false},
		{"zhipu-dev", "https://dev.bigmodel.cn/api/anthropic", nil, model.ProviderZhipu, false},
		{"minimax-io", "https://api.minimax.io/v1", nil, model.ProviderMinimax, false},
		{"minimax-i", "https://api.minimaxi.com/v1", nil, model.ProviderMinimax, false},
		{"opencode-ai", "https://api.opencode.ai/v1", nil, model.ProviderOpenCode, false},
		{"kimi-bare", "https://api.kimi.com/coding/v1", nil, model.ProviderKimi, false},
		{"kimi-moon-cn", "https://api.moonshot.cn/v1", nil, model.ProviderKimi, false},
		{"kimi-moon-com", "https://api.moonshot.com/v1", nil, model.ProviderKimi, false},

		// --- *. 后缀匹配 ---
		{"zhipu-subdomain", "https://proxy.bigmodel.cn/api/foo", nil, model.ProviderZhipu, false},
		{"zhipu-zai-sub", "https://chat.z.ai/foo", nil, model.ProviderZhipu, false},
		{"kimi-subdomain", "https://eu.kimi.com/v1", nil, model.ProviderKimi, false},

		// --- 大小写不敏感 ---
		{"kimi-uppercase-host", "https://API.KIMI.COM/v1", nil, model.ProviderKimi, false},
		{"zhipu-mixed-case", "https://Open.BigModel.cn/v1", nil, model.ProviderZhipu, false},

		// --- 端口 + 尾部斜杠 ---
		{"zhipu-with-port", "https://open.bigmodel.cn:443/api/anthropic/", nil, model.ProviderZhipu, false},
		{"kimi-with-port", "http://localhost:8080/v1", nil, "", true},

		// --- 用户自定义规则覆盖 ---
		{"user-rule-wins", "https://my-proxy.example.com/v1", []Rule{
			{Provider: model.ProviderKimi, Hosts: []string{"my-proxy.example.com"}, Priority: 100},
		}, model.ProviderKimi, false},
		{"user-rule-glob", "https://eu.my-proxy.example.com/v1", []Rule{
			{Provider: model.ProviderKimi, Hosts: []string{"*.my-proxy.example.com"}, Priority: 100},
		}, model.ProviderKimi, false},

		// --- path 前缀推断(配合 path-only 规则)---
		{"path-prefix-kimi", "https://random-host.example.org/coding/v1/models", []Rule{
			{Provider: model.ProviderKimi, PathPrefix: "/coding/v1", Priority: 50},
		}, model.ProviderKimi, false},
		{"path-prefix-no-match", "https://random-host.example.org/v1/chat", []Rule{
			{Provider: model.ProviderKimi, PathPrefix: "/coding/v1", Priority: 50},
		}, "", true},

		// --- priority tie-break ---
		{"priority-higher-wins", "https://collision.example.com/v1", []Rule{
			{Provider: model.ProviderKimi, Hosts: []string{"collision.example.com"}, Priority: 10},
			{Provider: model.ProviderZhipu, Hosts: []string{"collision.example.com"}, Priority: 20},
		}, model.ProviderZhipu, false},
		{"priority-zero-order", "https://another-collision.example.com/v1", []Rule{
			{Provider: model.ProviderKimi, Hosts: []string{"another-collision.example.com"}},
			{Provider: model.ProviderZhipu, Hosts: []string{"another-collision.example.com"}},
		}, model.ProviderKimi, false}, // 平级,声明顺序:Kimi 在前

		// --- 未知 host ---
		{"unknown-host", "https://unknown-provider.example.org/v1", nil, "", true},
		{"invalid-url", "not-a-url", nil, "", true},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := DetectFromBaseURL(c.url, c.extras)
			if (err != nil) != c.wantErr {
				t.Fatalf("err=%v, wantErr=%v", err, c.wantErr)
			}
			if !c.wantErr && got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
			if c.wantErr && c.name == "unknown-host" {
				var unk *ErrUnknownProvider
				if !errors.As(err, &unk) {
					t.Errorf("error type = %T, want ErrUnknownProvider", err)
				} else if !strings.Contains(unk.Host, "unknown-provider") {
					t.Errorf("diagnostic host = %q, should mention unknown-provider", unk.Host)
				}
			}
		})
	}
}

// TestMatchHostPattern 直单独验证 glob 解析的几个边界。
func TestMatchHostPattern(t *testing.T) {
	cases := []struct {
		pattern string
		host    string
		want    bool
	}{
		{"*.z.ai", "z.ai", true},
		{"*.z.ai", "api.z.ai", true},
		{"*.z.ai", "chat.z.ai", true},
		{"*.z.ai", "zai.example.com", false},
		{"*.z.ai", "bigmodel.cn", false},
		{"kimi.com", "kimi.com", true},
		{"kimi.com", "api.kimi.com", false},
		{"*.kimi.com", "kimi.com", true},
		{"*.kimi.com", "eu.api.kimi.com", true},
	}
	for _, c := range cases {
		t.Run(c.pattern+"_"+c.host, func(t *testing.T) {
			if got := matchHost(c.pattern, c.host); got != c.want {
				t.Errorf("matchHost(%q, %q) = %v, want %v", c.pattern, c.host, got, c.want)
			}
		})
	}
}

// TestDefaultRulesCoverAllProviders 守住"内建规则 = 唯一真源"约束:
// 每个 model.AllProviders() 都必须在 DefaultDetectionRules() 里至少有一条
// 规则覆盖。新增 provider 跳过这个测试,CI 拦下来。
func TestDefaultRulesCoverAllProviders(t *testing.T) {
	have := map[model.Provider]bool{}
	for _, r := range DefaultDetectionRules() {
		have[r.Provider] = true
	}
	for _, p := range model.AllProviders() {
		if !have[p] {
			t.Errorf("DefaultDetectionRules 缺 %q 的检测规则", p)
		}
	}
}

// TestEachProviderDeclaresItsOwnRules 守住"检测规则由各 provider 自己
// 声明"的约束:每个 model.AllProviders() 都对应一个 xxxRules() 函数,
// 且函数返回值非空。新增 provider 时:**实现 + xxxRules() + Build +
// authRegistry + DefaultDetectionRules 聚合** — 任何一项漏了都会被
// 下列两条之一拦住。
func TestEachProviderDeclaresItsOwnRules(t *testing.T) {
	type ruleFn func() []Rule
	cases := map[model.Provider]ruleFn{
		model.ProviderZhipu:    zhipuRules,
		model.ProviderMinimax:  minimaxRules,
		model.ProviderOpenCode: opencodeRules,
		model.ProviderKimi:     kimiRules,
	}
	for p, fn := range cases {
		t.Run(string(p), func(t *testing.T) {
			rs := fn()
			if len(rs) == 0 {
				t.Fatalf("xxxRules for %s returned empty slice", p)
			}
			for i, r := range rs {
				if r.Provider != p {
					t.Errorf("[%d] provider = %q, want %q", i, r.Provider, p)
				}
			}
		})
	}
	// 同步校验 DefaultDetectionRules 聚合后的 provider 集合 = AllProviders。
	have := map[model.Provider]bool{}
	for _, r := range DefaultDetectionRules() {
		have[r.Provider] = true
	}
	for _, p := range model.AllProviders() {
		if !have[p] {
			t.Errorf("DefaultDetectionRules 没聚合 %q 的规则", p)
		}
	}
}
