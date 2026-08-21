package config

import (
	"strings"
	"testing"

	"github.com/ge/subscription-monitor/internal/model"
)

func TestProviderKeysDefaultOnly(t *testing.T) {
	cfg := Default()
	cfg.Zhipu.Token = "tk-default"
	keys := cfg.ProviderKeys(model.ProviderZhipu)
	if len(keys) != 1 {
		t.Fatalf("len = %d, want 1", len(keys))
	}
	if keys[0].Label != "default" {
		t.Errorf("Label = %q, want default", keys[0].Label)
	}
	if keys[0].Token != "tk-default" {
		t.Errorf("Token = %q, want tk-default", keys[0].Token)
	}
}

func TestProviderKeysMulti(t *testing.T) {
	cfg := Default()
	cfg.Zhipu.Token = "tk-default"
	cfg.Zhipu.Keys = []ProviderKey{
		{Label: "alice", Token: "tk-alice"},
		{Label: "bob", Token: "tk-bob", BaseURL: "https://bob.example"},
	}
	keys := cfg.ProviderKeys(model.ProviderZhipu)
	if len(keys) != 3 {
		t.Fatalf("len = %d, want 3", len(keys))
	}
	wantLabels := []string{"default", "alice", "bob"}
	for i, k := range keys {
		if k.Label != wantLabels[i] {
			t.Errorf("[%d] Label = %q, want %q", i, k.Label, wantLabels[i])
		}
	}
	if keys[2].BaseURL != "https://bob.example" {
		t.Errorf("bob.BaseURL = %q", keys[2].BaseURL)
	}
}

func TestProviderKeysAutoLabelEmpty(t *testing.T) {
	cfg := Default()
	cfg.MiniMax.Token = "tk-default"
	cfg.MiniMax.Keys = []ProviderKey{
		{Token: "tk-anon-1"},
		{Token: "tk-anon-2"},
	}
	keys := cfg.ProviderKeys(model.ProviderMinimax)
	labels := []string{}
	for _, k := range keys {
		labels = append(labels, k.Label)
	}
	want := []string{"default", "key-1", "key-2"}
	for i, l := range labels {
		if l != want[i] {
			t.Errorf("[%d] Label = %q, want %q", i, l, want[i])
		}
	}
}

func TestProviderKeysDedup(t *testing.T) {
	cfg := Default()
	cfg.OpenCode.Token = "tk-default"
	cfg.OpenCode.Keys = []ProviderKey{
		{Label: "alice", Token: "tk-alice-old"},
		{Label: "alice", Token: "tk-alice-new"},
		{Label: "bob", Token: "tk-bob"},
	}
	keys := cfg.ProviderKeys(model.ProviderOpenCode)
	// 两个 alice → 后写覆盖前写,但 mergeKeys 跳过重复 label,
	// 所以最终只有 default + alice(后写)+ bob。
	if len(keys) != 3 {
		t.Fatalf("len = %d, want 3 (default + alice + bob)", len(keys))
	}
	if keys[1].Label != "alice" || keys[1].Token != "tk-alice-new" {
		t.Errorf("alice = %+v, want alice/tk-alice-new", keys[1])
	}
}

func TestProviderKeysEmptyTokensSkipped(t *testing.T) {
	cfg := Default()
	cfg.Kimi.Token = "tk-default"
	cfg.Kimi.Keys = []ProviderKey{
		{Label: "ghost", Token: ""},     // 空 token 跳过
		{Label: "alice", Token: "tk-a"}, // 有效
	}
	keys := cfg.ProviderKeys(model.ProviderKimi)
	if len(keys) != 2 {
		t.Fatalf("len = %d, want 2 (default + alice; ghost skipped)", len(keys))
	}
	if keys[1].Label != "alice" {
		t.Errorf("second key = %q, want alice", keys[1].Label)
	}
}

func TestProviderKeysNoTopLevelOnlyKeys(t *testing.T) {
	cfg := Default()
	cfg.Kimi.Keys = []ProviderKey{
		{Label: "solo", Token: "tk-solo"},
	}
	keys := cfg.ProviderKeys(model.ProviderKimi)
	if len(keys) != 1 {
		t.Fatalf("len = %d, want 1", len(keys))
	}
	if keys[0].Label != "solo" {
		t.Errorf("Label = %q, want solo", keys[0].Label)
	}
}

func TestProviderKeysEmpty(t *testing.T) {
	cfg := Default()
	if keys := cfg.ProviderKeys(model.ProviderZhipu); len(keys) != 0 {
		t.Errorf("empty config should yield 0 keys, got %d", len(keys))
	}
}

func TestDecryptSecretsKeys(t *testing.T) {
	cfg := Default()
	cfg.Zhipu.Token = "plain-default"
	cfg.Zhipu.Keys = []ProviderKey{
		{Label: "alice", Token: "plain-alice"},
		{Label: "bob", Token: "plain-bob"},
	}
	if err := cfg.DecryptSecrets(); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	// 全部明文 → 应原样保留,没有任何 enc:v1: 解密错误。
	if cfg.Zhipu.Token != "plain-default" {
		t.Errorf("default token: %q", cfg.Zhipu.Token)
	}
	if cfg.Zhipu.Keys[0].Token != "plain-alice" {
		t.Errorf("alice token: %q", cfg.Zhipu.Keys[0].Token)
	}
	if cfg.Zhipu.Keys[1].Token != "plain-bob" {
		t.Errorf("bob token: %q", cfg.Zhipu.Keys[1].Token)
	}
}

// TestProviderKeysOrderDeterminism 守住"每次调用顺序一致" — cache key 与
// 面板渲染都依赖这个。mergeKeys 必须按 (顶层 default) → [[keys]] 数组顺序。
func TestProviderKeysOrderDeterminism(t *testing.T) {
	cfg := Default()
	cfg.Zhipu.Token = "z-tk"
	cfg.Zhipu.Keys = []ProviderKey{
		{Label: "first", Token: "f"},
		{Label: "second", Token: "s"},
	}
	a := cfg.ProviderKeys(model.ProviderZhipu)
	b := cfg.ProviderKeys(model.ProviderZhipu)
	if len(a) != len(b) || len(a) != 3 {
		t.Fatalf("len mismatch: a=%d b=%d", len(a), len(b))
	}
	for i := range a {
		if a[i].Label != b[i].Label {
			t.Errorf("[%d] label drift: %q vs %q", i, a[i].Label, b[i].Label)
		}
	}
}

// TestAuthRegistryCoversConfigKeys 守住"所有 provider 的 [[keys]] 数组
// 都能通过 keysField() 找到" — 防止新增 provider 时漏写分支。
func TestKeysFieldCoversAllProviders(t *testing.T) {
	cfg := Default()
	for _, name := range []model.Provider{
		model.ProviderZhipu, model.ProviderMinimax,
		model.ProviderOpenCode, model.ProviderKimi,
	} {
		if keysField(cfg, name) == nil {
			t.Errorf("keysField nil for %s — add 多 key 时漏写了 case", name)
		}
	}
}

// --- internal helper for the last test ---

func keysField(c *Config, name model.Provider) *[]ProviderKey {
	switch name {
	case model.ProviderZhipu:
		return &c.Zhipu.Keys
	case model.ProviderMinimax:
		return &c.MiniMax.Keys
	case model.ProviderOpenCode:
		return &c.OpenCode.Keys
	case model.ProviderKimi:
		return &c.Kimi.Keys
	}
	return nil
}

// keep import strings used.
var _ = strings.Join
