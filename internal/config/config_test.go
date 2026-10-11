package config

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/ge/subscription-monitor/internal/model"
)

func TestProviderKeysBasic(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "zhipu", Label: "default", Token: "tk-default"},
		{Provider: "zhipu", Label: "alice", Token: "tk-alice"},
		{Provider: "minimax", Label: "default", Token: "mm"},
	}
	keys := cfg.ProviderKeys(model.ProviderZhipu)
	if len(keys) != 2 {
		t.Fatalf("len = %d, want 2", len(keys))
	}
	if keys[0].Label != "default" || keys[0].Token != "tk-default" {
		t.Errorf("[0] = %+v", keys[0])
	}
	if keys[1].Label != "alice" || keys[1].Token != "tk-alice" {
		t.Errorf("[1] = %+v", keys[1])
	}
}

func TestProviderKeysAutoLabelEmpty(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "minimax", Token: "tk-default"},
		{Provider: "minimax", Token: "tk-anon-1"},
		{Provider: "minimax", Token: "tk-anon-2"},
	}
	keys := cfg.ProviderKeys(model.ProviderMinimax)
	labels := []string{}
	for _, k := range keys {
		labels = append(labels, k.Label)
	}
	want := []string{"default", "key-1", "key-2"}
	if len(labels) != len(want) {
		t.Fatalf("labels = %v, want %v", labels, want)
	}
	for i, l := range labels {
		if l != want[i] {
			t.Errorf("[%d] Label = %q, want %q", i, l, want[i])
		}
	}
}

// 未命名账号的第一个占 default,且不会覆盖显式 label。
func TestProviderKeysAutoLabelNoOverride(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "zhipu", Label: "key-1", Token: "explicit"},
		{Provider: "zhipu", Token: "anon"},
	}
	keys := cfg.ProviderKeys(model.ProviderZhipu)
	if len(keys) != 2 {
		t.Fatalf("len = %d, want 2", len(keys))
	}
	if keys[0].Label != "key-1" || keys[0].Token != "explicit" {
		t.Errorf("explicit key = %+v", keys[0])
	}
	if keys[1].Label != "default" || keys[1].Token != "anon" {
		t.Errorf("auto key = %+v", keys[1])
	}
}

func TestProviderKeysDedup(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "opencode", Label: "alice", Token: "tk-alice-old"},
		{Provider: "opencode", Label: "alice", Token: "tk-alice-new"},
		{Provider: "opencode", Label: "bob", Token: "tk-bob"},
	}
	keys := cfg.ProviderKeys(model.ProviderOpenCode)
	if len(keys) != 2 {
		t.Fatalf("len = %d, want 2 (alice + bob)", len(keys))
	}
	if keys[0].Label != "alice" || keys[0].Token != "tk-alice-new" {
		t.Errorf("alice = %+v, want alice/tk-alice-new", keys[0])
	}
}

func TestProviderKeysEmptyTokensSkipped(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "kimi", Label: "default", Token: "tk-default"},
		{Provider: "kimi", Label: "ghost", Token: ""}, // 空 token 跳过
		{Provider: "kimi", Label: "alice", Token: "tk-a"},
	}
	keys := cfg.ProviderKeys(model.ProviderKimi)
	if len(keys) != 2 {
		t.Fatalf("len = %d, want 2 (default + alice; ghost skipped)", len(keys))
	}
	if keys[1].Label != "alice" {
		t.Errorf("second key = %q, want alice", keys[1].Label)
	}
}

func TestProviderKeysEmpty(t *testing.T) {
	cfg := Default()
	if keys := cfg.ProviderKeys(model.ProviderZhipu); len(keys) != 0 {
		t.Errorf("empty config should yield 0 keys, got %d", len(keys))
	}
}

func TestAccountsOfCoversGotify(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{{Provider: "gotify", Label: "default", Token: "enc:x", BaseURL: "https://g.example"}}
	got := cfg.AccountsOf("gotify")
	if len(got) != 1 || got[0].BaseURL != "https://g.example" {
		t.Fatalf("gotify accounts = %+v", got)
	}
}

func TestDecryptSecretsAccounts(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "zhipu", Label: "default", Token: "plain-default"},
		{Provider: "zhipu", Label: "alice", Token: "plain-alice"},
	}
	if err := cfg.DecryptSecrets(); err != nil {
		t.Fatalf("decrypt: %v", err)
	}
	// 全部明文 → 应原样保留,没有任何 enc:v1: 解密错误。
	if cfg.Accounts[0].Token != "plain-default" || cfg.Accounts[1].Token != "plain-alice" {
		t.Errorf("plaintext tokens mutated: %+v", cfg.Accounts)
	}
}

func TestUpsertAccountMergesFields(t *testing.T) {
	cfg := Default()
	cfg.UpsertAccount(Account{Provider: "zhipu", Label: "", Token: "tk"})
	if cfg.Accounts[0].Label != DefaultLabel {
		t.Fatalf("empty label not normalized: %+v", cfg.Accounts[0])
	}
	// 只更新 base_url,不能清掉 token。
	cfg.UpsertAccount(Account{Provider: "zhipu", Label: "default", BaseURL: "https://x"})
	if cfg.Accounts[0].Token != "tk" || cfg.Accounts[0].BaseURL != "https://x" {
		t.Fatalf("merge failed: %+v", cfg.Accounts[0])
	}
	// 只更新 token,不能清掉 base_url。
	cfg.UpsertAccount(Account{Provider: "zhipu", Label: "default", Token: "tk2"})
	if cfg.Accounts[0].Token != "tk2" || cfg.Accounts[0].BaseURL != "https://x" {
		t.Fatalf("merge failed: %+v", cfg.Accounts[0])
	}
	if len(cfg.Accounts) != 1 {
		t.Fatalf("upsert duplicated: %+v", cfg.Accounts)
	}
}

func TestRemoveAccount(t *testing.T) {
	cfg := Default()
	cfg.Accounts = []Account{
		{Provider: "zhipu", Label: "default", Token: "a"},
		{Provider: "zhipu", Label: "alice", Token: "b"},
	}
	if !cfg.RemoveAccount("zhipu", "alice") {
		t.Fatal("remove alice should return true")
	}
	if cfg.FindAccount("zhipu", "alice") != nil {
		t.Error("alice still present")
	}
	if !cfg.RemoveAccount("zhipu", "") {
		t.Fatal("empty label should remove default")
	}
	if cfg.RemoveAccount("zhipu", "ghost") {
		t.Error("remove ghost should return false")
	}

	// 无 label 的账号用空 label 删除。
	cfg2 := Default()
	cfg2.Accounts = []Account{{Provider: "kimi", Token: "c"}}
	if !cfg2.RemoveAccount("kimi", "default") {
		t.Error("unlabeled account should be removable via default")
	}
}

// --- 迁移测试 ---

// useConfigDir 把配置目录重定向到 dir:POSIX 走 XDG_CONFIG_HOME,Windows 走
// %AppData% —— 只设一个平台的话,另一个平台跑测试会写到真实配置目录。
func useConfigDir(t *testing.T, dir string) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", dir)
	t.Setenv("AppData", dir)
}

// writeConfig 把 raw 写到配置目录下的 config.toml,返回 Load 结果。
func writeConfig(t *testing.T, raw string) *Config {
	t.Helper()
	dir := t.TempDir()
	useConfigDir(t, dir)
	if err := os.MkdirAll(filepath.Join(dir, "submon"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "submon", "config.toml"), []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestMigrateLegacyProviderTables(t *testing.T) {
	cfg := writeConfig(t, `
[zhipu]
token = "enc:tk-zhipu"
base_url = "https://open.bigmodel.cn/api/anthropic"

[[zhipu.keys]]
label = "alice"
token = "enc:tk-alice"
base_url = "https://alice.example"

[[zhipu.keys]]
token = "enc:tk-anon"

[minimax]
token = "enc:tk-mm"

[notify.gotify]
url = "https://gotify.example"
app_token = "enc:tk-gotify"
`)
	// zhipu: default + alice + 匿名(key-1)
	zhipu := cfg.ProviderKeys(model.ProviderZhipu)
	if len(zhipu) != 3 {
		t.Fatalf("zhipu accounts = %+v", zhipu)
	}
	if zhipu[0].Label != "default" || zhipu[0].BaseURL == "" {
		t.Errorf("default account = %+v", zhipu[0])
	}
	if zhipu[1].Label != "alice" || zhipu[1].BaseURL != "https://alice.example" {
		t.Errorf("alice account = %+v", zhipu[1])
	}
	if zhipu[2].Label != "key-1" || zhipu[2].Token != "enc:tk-anon" {
		t.Errorf("anonymous account = %+v", zhipu[2])
	}
	if mm := cfg.ProviderKeys(model.ProviderMinimax); len(mm) != 1 || mm[0].Token != "enc:tk-mm" {
		t.Errorf("minimax accounts = %+v", mm)
	}
	url, token := cfg.GotifyCredential()
	if url != "https://gotify.example" || token != "enc:tk-gotify" {
		t.Errorf("gotify credential = %q / %q", url, token)
	}
}

func TestMigrateLegacyBaseURLOnly(t *testing.T) {
	cfg := writeConfig(t, `
[zhipu]
base_url = "https://proxy.example"
`)
	// 无 token 的占位账号不参与取数,但 base_url 仍可被默认路径读到。
	if keys := cfg.ProviderKeys(model.ProviderZhipu); len(keys) != 0 {
		t.Errorf("base_url-only account should not be a usable key: %+v", keys)
	}
	a := cfg.FindAccount("zhipu", "default")
	if a == nil || a.BaseURL != "https://proxy.example" {
		t.Fatalf("base_url-only account lost: %+v", a)
	}
}

func TestMigrateDoesNotOverrideNewAccounts(t *testing.T) {
	cfg := writeConfig(t, `
[[accounts]]
provider = "zhipu"
label = "default"
token = "new-token"

[zhipu]
token = "old-token"
`)
	keys := cfg.ProviderKeys(model.ProviderZhipu)
	if len(keys) != 1 || keys[0].Token != "new-token" {
		t.Fatalf("new account should win: %+v", keys)
	}
}

func TestLoadSaveRoundTripNewSchema(t *testing.T) {
	cfg := writeConfig(t, `
[[accounts]]
provider = "zhipu"
label = "default"
token = "enc:x"
base_url = "https://z.example"

[[accounts]]
provider = "gotify"
label = "default"
token = "enc:g"
base_url = "https://g.example"
`)
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if len(again.Accounts) != 2 {
		t.Fatalf("accounts = %+v", again.Accounts)
	}
	if again.Accounts[0].BaseURL != "https://z.example" || again.Accounts[1].Provider != "gotify" {
		t.Errorf("round-trip mismatch: %+v", again.Accounts)
	}
}

func TestLegacyGotifyNotMigratedWhenAccountExists(t *testing.T) {
	cfg := writeConfig(t, `
[[accounts]]
provider = "gotify"
label = "default"
token = "new-gotify"

[notify.gotify]
url = "https://old.example"
app_token = "old-gotify"
`)
	url, token := cfg.GotifyCredential()
	if token != "new-gotify" || url != "" {
		// 账号已存在 → 旧格式整条忽略,新账号优先。
		t.Errorf("gotify = %q / %q", url, token)
	}
}

// 配置编码器替换必须保留平台路径、多行凭据和全部已有设置。
func TestConfigCodecPreservesValues(t *testing.T) {
	t.Setenv("SUBMON_LAYOUT", "")
	cfg := writeConfig(t, `
[[accounts]]
provider = "kimi"
label = "工作账号"
token = """line1
line2"quoted""""
base_url = "https://example.test/api"

[cache]
ttl_seconds = 123
path = 'E:\缓存\submon\cache.json'

[notify]
warn_below_pct = 12.5
crit_below_pct = 2.5
remind_before = "45m"
waste_notify_pct = 34.5
waste_urgent_pct = 67.5

[notify.gotify]
priority = 7

[statusline]
layout = "c"

[unused]
# TOML 1.1:支持多行内联表,未知字段依旧忽略。
metadata = {
  date = 2026-10-11,
  text = "未知字段",
}
`)
	if cfg.Cache.Path != `E:\缓存\submon\cache.json` || cfg.Accounts[0].Token != "line1\nline2\"quoted\"" {
		t.Fatalf("配置解析丢失路径或凭据: %+v", cfg)
	}
	if err := Save(cfg); err != nil {
		t.Fatal(err)
	}
	again, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(again, cfg) {
		t.Fatalf("配置往返不一致: %+v != %+v", again, cfg)
	}
}
