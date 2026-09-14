package cmd

import (
	"testing"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

// TestTargetProvidersDefaultsHideUnconfigured 默认参数 (无 provider) 时,
// 只返回已配置的 provider — 避免面板里出现 "X 未配置" 的噪声条目。
// 显式传 provider 名时仍然返回(未配置的情况交由 RunE 显示原因)。
func TestTargetProvidersDefaultsHideUnconfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{Provider: "zhipu", Label: "default", Token: "sk-zhipu"}, // 已配
		{Provider: "minimax", Label: "default", Token: "sk-mm"},  // 已配
	}
	// OpenCode 与 Kimi 故意未配。

	got, err := targetProviders(nil, cfg)
	if err != nil {
		t.Fatal(err)
	}
	want := []model.Provider{model.ProviderZhipu, model.ProviderMinimax}
	if len(got) != len(want) {
		t.Fatalf("len=%d, want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Errorf("[%d] got %q, want %q", i, got[i], want[i])
		}
	}

	// 显式传未配置的 provider — 仍然返回,RunE 负责给原因。
	got, err = targetProviders([]string{"kimi"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != model.ProviderKimi {
		t.Errorf("explicit unconfigured: got %v, want [kimi]", got)
	}

	// 显式传已配置的 — 也返回。
	got, err = targetProviders([]string{"zhipu"}, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != model.ProviderZhipu {
		t.Errorf("explicit configured: got %v, want [zhipu]", got)
	}

	// 未知 provider 报错。
	if _, err := targetProviders([]string{"nope"}, cfg); err == nil {
		t.Error("unknown provider should error")
	}
}

// TestProviderConfigured 守住 "已配 token" 判定的边界。
func TestProviderConfigured(t *testing.T) {
	cfg := config.Default()
	// 全空 — 都不算配置。
	for _, p := range model.AllProviders() {
		if providerConfigured(p, cfg) {
			t.Errorf("%s reported configured on empty config", p)
		}
	}
	// 每个 provider 单独配账号,只有它自己算已配。
	cfg.Accounts = []config.Account{{Provider: "zhipu", Label: "default", Token: "sk"}}
	if !providerConfigured(model.ProviderZhipu, cfg) {
		t.Error("zhipu not detected")
	}
	if providerConfigured(model.ProviderMinimax, cfg) {
		t.Error("minimax should not be configured")
	}

	for _, p := range model.AllProviders() {
		cfg.UpsertAccount(config.Account{Provider: string(p), Label: "default", Token: "sk"})
	}
	for _, p := range model.AllProviders() {
		if !providerConfigured(p, cfg) {
			t.Errorf("%s should be configured", p)
		}
	}

	// BaseURL 不参与判定(有 base_url 没 token 的账号不算已配置)。
	cfg2 := config.Default()
	cfg2.Accounts = []config.Account{
		{Provider: "zhipu", Label: "default", BaseURL: "https://x"},
		{Provider: "kimi", Label: "default", BaseURL: "https://x"},
	}
	if providerConfigured(model.ProviderZhipu, cfg2) {
		t.Error("base_url alone should not count as configured")
	}
	if providerConfigured(model.ProviderKimi, cfg2) {
		t.Error("kimi base_url alone should not count as configured")
	}
}
