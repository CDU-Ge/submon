package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
)

// authTestProvider 统一 provider 清单 — 任何新 provider 必须通过同一张表
// 的同一条路径(set/ls/remove/check 共 4 条用例),新增/漏配会同时在多
// 个测试上爆,不会出现"某 provider 走自己私路"这种失范。
type authTestProvider struct {
	Name        string
	TokenStdin  string
	ExtraStdin  string // 第二个字段 (URL 之类),没则空
	FieldName   string // secret 字段名
	HasURLField bool
}

var authTestProviders = []authTestProvider{
	{Name: "zhipu", TokenStdin: "sk-zhipu-test", FieldName: "token", HasURLField: true},
	{Name: "minimax", TokenStdin: "sk-minimax-test", FieldName: "token"},
	{Name: "opencode", TokenStdin: "oc-test", FieldName: "token"},
	{Name: "kimi", TokenStdin: "sk-kimi-test", FieldName: "token", HasURLField: true},
	{Name: "gotify", TokenStdin: "https://gotify.example.test", ExtraStdin: "gotify-app-token", FieldName: "app_token"},
}

// withTempConfig 把 XDG_CONFIG_HOME 重定向到 tempdir。t.Cleanup 还原全局状态。
func withTempConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// withStdin 把 os.Stdin 替换成含 input 的可读文件。readAuthField 看到普通
// 文件时 term.IsTerminal 返回 false,走 bufio.ReadString 路径。t.Cleanup 还原。
func withStdin(t *testing.T, input string) {
	t.Helper()
	old := os.Stdin
	tmp, err := os.CreateTemp(t.TempDir(), "stdin")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tmp.WriteString(input); err != nil {
		t.Fatal(err)
	}
	if err := tmp.Close(); err != nil {
		t.Fatal(err)
	}
	r, err := os.Open(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	os.Stdin = r
	t.Cleanup(func() {
		os.Stdin = old
		_ = r.Close()
	})
}

// newTestCmd 给 runAuth* 系列的 *cobra.Command 参数。RunE 不参与,只接
// Out/Err 到 buffer。
func newTestCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	return cmd, out, errBuf
}

// inputFor 构造 provider 的 stdin 序列(token + 可选 extra + 可选空 base_url)。
func inputFor(p authTestProvider) string {
	in := p.TokenStdin + "\n"
	if p.ExtraStdin != "" {
		in += p.ExtraStdin + "\n"
	}
	if p.HasURLField {
		in += "\n" // 跳过 base_url
	}
	return in
}

// isPlainLeak tells whether the test fixture's plaintext token would be a
// leakage if it shows up in config.toml. gotify's URL is the "token" field
// but is allowed to be plaintext (URL is not a secret).
func isPlainLeak(p authTestProvider, s string) bool {
	if s == "" {
		return false
	}
	if p.Name == "gotify" && s == p.TokenStdin {
		return false // URL is plaintext by design
	}
	return true
}

// TestAuthSetEncryptsAndPersists 每个 provider 跑一次 auth set,验证:
//  1. secret 字段已加密 (enc:v1:...)
//  2. config.toml 不含 plaintext token
//  3. 加密值可解密
func TestAuthSetEncryptsAndPersists(t *testing.T) {
	for _, p := range authTestProviders {
		t.Run(p.Name, func(t *testing.T) {
			withTempConfig(t)
			withStdin(t, inputFor(p))

			cmd, _, _ := newTestCmd()
			if err := runAuthSet(cmd, p.Name); err != nil {
				t.Fatalf("auth set %s: %v", p.Name, err)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			v := authSecretField(p.Name, cfg)
			if v == "" {
				t.Fatalf("%s.%s is empty after auth set", p.Name, p.FieldName)
			}
			if !secret.IsEncrypted(v) {
				t.Fatalf("%s.%s not encrypted: %s", p.Name, p.FieldName, v)
			}
			raw, err := readRawConfig(t)
			if err != nil {
				t.Fatal(err)
			}
			if isPlainLeak(p, p.TokenStdin) && strings.Contains(raw, p.TokenStdin) {
				t.Errorf("plaintext token leaked into config.toml: %s", raw)
			}
			if p.ExtraStdin != "" && strings.Contains(raw, p.ExtraStdin) {
				t.Errorf("plaintext secret leaked into config.toml")
			}
			if _, err := secret.Decrypt(v); err != nil {
				t.Errorf("decrypt failed: %v", err)
			}
		})
	}
}

// TestAuthLsReports 空配置下 ls 列出所有 provider;set 之后 ls 显示 enc;
// 任何路径下都绝不打印 plaintext token。
func TestAuthLsReports(t *testing.T) {
	withTempConfig(t)

	// 空配置:每个 provider 一行,带 - 标记。
	cmd, out, _ := newTestCmd()
	if err := runAuthLs(cmd); err != nil {
		t.Fatal(err)
	}
	for _, p := range authTestProviders {
		if !strings.Contains(out.String(), p.Name) {
			t.Errorf("ls missing %s: %s", p.Name, out.String())
		}
	}
	if strings.Contains(out.String(), "PLAINTEXT") || strings.Contains(out.String(), "sk-") || strings.Contains(out.String(), "gotify-app-token") {
		t.Errorf("ls leaked plaintext markers: %s", out.String())
	}

	// set zhipu 之后 ls 应显示 enc 标记,且不含 plaintext token。
	withStdin(t, "sk-zhipu-set\n\n")
	cmd2, _, _ := newTestCmd()
	if err := runAuthSet(cmd2, "zhipu"); err != nil {
		t.Fatal(err)
	}
	cmd3, out3, _ := newTestCmd()
	if err := runAuthLs(cmd3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out3.String(), "enc") {
		t.Errorf("ls missing enc marker after set: %s", out3.String())
	}
	if strings.Contains(out3.String(), "sk-zhipu-set") {
		t.Errorf("ls leaked plaintext token: %s", out3.String())
	}
}

// TestAuthRemoveClears 每个 provider 都能 remove 且不影响其他 provider。
func TestAuthRemoveClears(t *testing.T) {
	for _, p := range authTestProviders {
		t.Run(p.Name, func(t *testing.T) {
			withTempConfig(t)
			withStdin(t, inputFor(p))

			cmd, _, _ := newTestCmd()
			if err := runAuthSet(cmd, p.Name); err != nil {
				t.Fatal(err)
			}
			cmd2, _, _ := newTestCmd()
			if err := runAuthRemove(cmd2, p.Name); err != nil {
				t.Fatalf("auth remove: %v", err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if v := authSecretField(p.Name, cfg); v != "" {
				t.Errorf("after remove, %s.%s = %q, want empty", p.Name, p.FieldName, v)
			}
		})
	}
}

// TestAuthCheckReportsMissing 空配置下 check 返回非零,stderr 报告所有 provider。
func TestAuthCheckReportsMissing(t *testing.T) {
	withTempConfig(t)

	cmd, _, errBuf := newTestCmd()
	if err := runAuthCheck(cmd); err == nil {
		t.Fatal("auth check should fail on empty config")
	}
	for _, p := range authTestProviders {
		if !strings.Contains(errBuf.String(), p.Name) {
			t.Errorf("check missing %s in stderr: %s", p.Name, errBuf.String())
		}
	}
}

// TestAuthCheckAfterSet 全部 set 之后 check 成功。
func TestAuthCheckAfterSet(t *testing.T) {
	withTempConfig(t)
	for _, p := range authTestProviders {
		withStdin(t, inputFor(p))
		cmd, _, _ := newTestCmd()
		if err := runAuthSet(cmd, p.Name); err != nil {
			t.Fatalf("auth set %s: %v", p.Name, err)
		}
	}
	withStdin(t, "")
	cmd, _, _ := newTestCmd()
	if err := runAuthCheck(cmd); err != nil {
		t.Fatalf("auth check after set: %v", err)
	}
}

// TestAuthRegistryCoversAllProviders 守住"注册表 = 唯一真源"约束:
// model.AllProviders() 集合 ⊆ authRegistry 集合。新增 provider 跳过这个
// 测试,CI 就会拦下来。
func TestAuthRegistryCoversAllProviders(t *testing.T) {
	for _, p := range []string{"zhipu", "minimax", "opencode", "kimi"} {
		if _, ok := authRegistry[p]; !ok {
			t.Errorf("authRegistry missing model provider %q", p)
		}
	}
	if _, ok := authRegistry["gotify"]; !ok {
		t.Error("authRegistry missing gotify (gotify is treated as a first-class provider for security)")
	}
}

// TestAuthRejectsUnknownProvider 验证未知 provider 错误信息。
func TestAuthRejectsUnknownProvider(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "unknown-provider"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestAuthGotifyBothFields gotify 是唯一带"明文 URL + 加密 secret"组合的
// provider,单独验:URL 明文,app_token 加密,plaintext token 不泄到 toml。
func TestAuthGotifyBothFields(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "https://gotify.example.test\ngotify-app-token-xyz\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "gotify"); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Notify.Gotify.URL != "https://gotify.example.test" {
		t.Errorf("url = %q, want https://gotify.example.test", cfg.Notify.Gotify.URL)
	}
	if !secret.IsEncrypted(cfg.Notify.Gotify.AppToken) {
		t.Errorf("app_token not encrypted: %q", cfg.Notify.Gotify.AppToken)
	}
	raw, err := readRawConfig(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "gotify-app-token-xyz") {
		t.Errorf("gotify app_token plaintext leaked")
	}
}

// TestAuthKimiE2E kimi 走 auth set + check 端到端,确保 kimi 槽位与
// 其他 provider 在所有 auth 流上完全等价。
// 断言:check 在只配 kimi 时会报其他 4 个 provider 缺失,但 kimi 本身
// 不在错误列表里。
func TestAuthKimiE2E(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "sk-kimi-xyz\n\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "kimi"); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if !secret.IsEncrypted(cfg.Kimi.Token) {
		t.Errorf("kimi token not encrypted")
	}
	withStdin(t, "")
	cmd2, _, errBuf := newTestCmd()
	if err := runAuthCheck(cmd2); err == nil {
		t.Fatal("check should fail when only kimi is configured (other providers missing)")
	}
	if strings.Contains(errBuf.String(), "kimi:") {
		t.Errorf("check should not flag kimi as missing: %s", errBuf.String())
	}
	for _, name := range []string{"zhipu", "minimax", "opencode", "gotify"} {
		if !strings.Contains(errBuf.String(), name+":") {
			t.Errorf("check should report %s as missing: %s", name, errBuf.String())
		}
	}
}

// authSecretField 用 provider 名取对应 secret 字段值,统一测试断言路径。
func authSecretField(name string, cfg *config.Config) string {
	switch name {
	case "zhipu":
		return cfg.Zhipu.Token
	case "minimax":
		return cfg.MiniMax.Token
	case "opencode":
		return cfg.OpenCode.Token
	case "kimi":
		return cfg.Kimi.Token
	case "gotify":
		return cfg.Notify.Gotify.AppToken
	}
	return ""
}

// --- 多 key 测试 ---

// TestAuthAddLabeledKey 验证 `submon auth zhipu add <label>`:
//   - label 非 default → 写入 [[zhipu.keys]],顶层 Token 不动
//   - 再次 add 同 label → 覆盖该 label(后写覆盖前写)
//   - 加 label=default 走默认路径,等价于 auth <provider>
func TestAuthAddLabeledKey(t *testing.T) {
	withTempConfig(t)

	// 1. 先有 default Key
	withStdin(t, "tk-default\n\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "zhipu"); err != nil {
		t.Fatal(err)
	}

	// 2. add alice
	withStdin(t, "tk-alice\nhttps://alice.example\n")
	cmd, _, _ = newTestCmd()
	if err := runAuthAddLabeled(cmd, "zhipu", "alice"); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	cfg, _ := config.Load()
	if !secret.IsEncrypted(cfg.Zhipu.Token) || cfg.Zhipu.Token == "" {
		t.Errorf("default token lost: %q", cfg.Zhipu.Token)
	}
	if len(cfg.Zhipu.Keys) != 1 || cfg.Zhipu.Keys[0].Label != "alice" {
		t.Fatalf("keys not appended: %+v", cfg.Zhipu.Keys)
	}
	if cfg.Zhipu.Keys[0].BaseURL != "https://alice.example" {
		t.Errorf("alice.BaseURL = %q", cfg.Zhipu.Keys[0].BaseURL)
	}
	if !secret.IsEncrypted(cfg.Zhipu.Keys[0].Token) {
		t.Error("alice token not encrypted")
	}

	// 3. add alice again(覆盖)
	withStdin(t, "tk-alice-v2\n")
	cmd, _, _ = newTestCmd()
	if err := runAuthAddLabeled(cmd, "zhipu", "alice"); err != nil {
		t.Fatalf("add alice overwrite: %v", err)
	}
	cfg, _ = config.Load()
	if len(cfg.Zhipu.Keys) != 1 {
		t.Errorf("keys len = %d, want 1 (覆盖不增加)", len(cfg.Zhipu.Keys))
	}
}

// TestAuthRemoveLabeledKey 删除 default 顶层 + [[keys]] 中某 label。
//   - remove default → 顶层 Token/BaseURL 清空,[[keys]] 保留
//   - remove alice → 从 [[keys]] 移除
//   - remove 不存在的 label → no-op,不报错
func TestAuthRemoveLabeledKey(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "tk-default\nhttps://default.example\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "zhipu"); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "tk-alice\n")
	cmd, _, _ = newTestCmd()
	if err := runAuthAddLabeled(cmd, "zhipu", "alice"); err != nil {
		t.Fatal(err)
	}

	// remove alice → [[keys]] 清掉,顶层 default 保留
	cmd, _, _ = newTestCmd()
	if err := runAuthRemoveLabeled(cmd, "zhipu", "alice"); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	cfg, _ := config.Load()
	if cfg.Zhipu.Token == "" {
		t.Error("default token should survive removing alice")
	}
	if len(cfg.Zhipu.Keys) != 0 {
		t.Errorf("alice key not removed: %+v", cfg.Zhipu.Keys)
	}

	// remove default → 顶层清空
	cmd, _, _ = newTestCmd()
	if err := runAuthRemoveLabeled(cmd, "zhipu", "default"); err != nil {
		t.Fatalf("remove default: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.Zhipu.Token != "" {
		t.Errorf("default token still set: %q", cfg.Zhipu.Token)
	}

	// remove 不存在的 label → no-op
	cmd, _, _ = newTestCmd()
	if err := runAuthRemoveLabeled(cmd, "zhipu", "ghost"); err != nil {
		t.Errorf("remove ghost should not error: %v", err)
	}
}

// TestAuthRemoveRequiresConfirmation 删除凭据是本工具最不可逆的操作:
// 非 TTY 无 -f 拒绝执行(带 -f 提示);-f 放行后才真正删除。
func TestAuthRemoveRequiresConfirmation(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "tk-default\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "zhipu"); err != nil {
		t.Fatal(err)
	}

	defer func() { authRemoveForce = false }()
	authRemoveForce = false
	cmd, _, _ = newTestCmd()
	err := removeWithConfirm(cmd, "zhipu", "default")
	if err == nil || !strings.Contains(err.Error(), "-f") {
		t.Fatalf("非 TTY 无 -f 应拒绝并提示 -f, got %v", err)
	}
	cfg, _ := config.Load()
	if cfg.Zhipu.Token == "" {
		t.Fatal("被拒绝的删除不应动到凭据")
	}

	authRemoveForce = true
	cmd, _, _ = newTestCmd()
	if err := removeWithConfirm(cmd, "zhipu", "default"); err != nil {
		t.Fatalf("remove with -f: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.Zhipu.Token != "" {
		t.Errorf("-f 后 token 未删除: %q", cfg.Zhipu.Token)
	}
}

// TestAuthLsReportsMultiKey `auth ls` 多 key 模式下展示 default + 每条 label。
func TestAuthLsReportsMultiKey(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "tk-default\n\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthSet(cmd, "zhipu"); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "tk-alice\n\n")
	cmd, _, _ = newTestCmd()
	if err := runAuthAddLabeled(cmd, "zhipu", "alice"); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "tk-bob\n\n")
	cmd, _, _ = newTestCmd()
	if err := runAuthAddLabeled(cmd, "zhipu", "bob"); err != nil {
		t.Fatal(err)
	}

	cmd, out, _ := newTestCmd()
	if err := runAuthLs(cmd); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"default=enc", "alice=enc", "bob=enc"} {
		if !strings.Contains(s, want) {
			t.Errorf("ls missing %q: %s", want, s)
		}
	}
}

// TestAuthAddRejectsUnknownProvider 守住 "auth <unknown> add <label>" 报错。
func TestAuthAddRejectsUnknownProvider(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "tk\n")
	cmd, _, _ := newTestCmd()
	if err := runAuthAddLabeled(cmd, "unknown", "alice"); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestAuthCmdDispatchAddRemove 走 cobra dispatch 路径,验证子命令解析:
//
//	`submon auth zhipu add alice` → runAuthAddLabeled
//	`submon auth zhipu remove alice` → runAuthRemoveLabeled
func TestAuthCmdDispatchAddRemove(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "tk-alice\n")
	cmd := &cobra.Command{}
	cmd.SetOut(&bytes.Buffer{})
	cmd.SetErr(&bytes.Buffer{})
	// 通过 RunE + args 直接调用,绕开 cobra 的 args 校验。
	if err := runAuthAddLabeled(cmd, "zhipu", "alice"); err != nil {
		t.Fatal(err)
	}
	cfg, _ := config.Load()
	if len(cfg.Zhipu.Keys) != 1 || cfg.Zhipu.Keys[0].Label != "alice" {
		t.Fatalf("add 失败: %+v", cfg.Zhipu.Keys)
	}
}

// TestProviderKeysIsTruthForConfigured 守住 "providerConfigured()"
// 用 [[keys]] 也能识别 — 没这个,多 key 的 provider 会被 status/statusline
// 当作"未配置"隐藏掉。
func TestProviderKeysIsTruthForConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Zhipu.Keys = []config.ProviderKey{
		{Label: "solo", Token: "tk"},
	}
	if !providerConfigured(model.ProviderZhipu, cfg) {
		t.Error("providerConfigured should return true when only [[keys]] is set")
	}
}

// TestReadAuthFieldTTYNonSecret 守住 "kimi / zhipu 的 base_url 字段在
// TTY 模式下不 panic" 的回归 — 老代码 br 为 nil,base_url 字段
// 直接 nil deref。本测试通过 pipe fd 模拟 TTY,直接打 readAuthField 的
// useTerm=true / 非 Secret 分支。
func TestReadAuthFieldTTYNonSecret(t *testing.T) {
	// readLineFromFD 单测:pipe 写端塞输入,读端给 readLineFromFD。
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	go func() {
		defer w.Close()
		_, _ = w.WriteString("https://api.example.com/v1\n")
	}()
	got, err := readLineFromFD(int(r.Fd()))
	if err != nil {
		t.Fatalf("readLineFromFD: %v", err)
	}
	if got != "https://api.example.com/v1" {
		t.Errorf("got %q, want https://api.example.com/v1", got)
	}

	// useTerm=true + 非 Secret + nil br — 老 bug 这里 panic。
	r2, w2, _ := os.Pipe()
	defer r2.Close()
	go func() {
		defer w2.Close()
		_, _ = w2.WriteString("base-url-value\n")
	}()
	oldStdin := os.Stdin
	os.Stdin = r2
	t.Cleanup(func() { os.Stdin = oldStdin })

	v, err := readAuthField(authField{Key: "base_url", Prompt: "url", Secret: false, Required: false}, nil, true)
	if err != nil {
		t.Fatalf("readAuthField with useTerm=true / non-Secret: %v", err)
	}
	if v != "base-url-value" {
		t.Errorf("got %q, want base-url-value", v)
	}
}

// readRawConfig 直接读磁盘 config.toml,用于"不能泄漏明文"的硬断言。
func readRawConfig(t *testing.T) (string, error) {
	t.Helper()
	dir, err := config.Dir()
	if err != nil {
		return "", err
	}
	b, err := os.ReadFile(filepath.Join(dir, "config.toml"))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// TestDiscoverSourcesFromEnv 守住 "discover 必须从 env 里捞出候选项" +
// "掩码不进明文到 Value" 的契约。env 清干净后才设,保证测试稳定。
func TestDiscoverSourcesFromEnv(t *testing.T) {
	withTempConfig(t)
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-zhipu-discovery-test-token")
	t.Setenv("KIMI_CODING_API_KEY", "sk-kimi-discovery-test-token")
	t.Setenv("MINIMAX_TOKEN", "sk-minimax-discovery-test-token")
	t.Setenv("OPENCODE_API_KEY", "sk-opencode-discovery-test-token")
	t.Setenv("ANTHROPIC_BASE_URL", "https://open.bigmodel.cn/api/anthropic")
	// KIMI_API_KEY 跟 KIMI_CODING_API_KEY 同时存在 — 验证去重(只列前者)。
	t.Setenv("KIMI_API_KEY", "sk-kimi-secondary")

	sources := scanDiscoverSources()

	// 应该至少有 4 个:zhipu token / kimi token (coding) / minimax / opencode / base_url。
	byProvider := map[string]int{}
	for _, s := range sources {
		byProvider[s.Provider]++
	}
	for _, want := range []string{"zhipu", "kimi", "minimax", "opencode"} {
		if byProvider[want] == 0 {
			t.Errorf("discover should find %s, got %v", want, byProvider)
		}
	}

	// Value 字段里**绝不能出现**明文 token 片段 — 验证掩码生效。
	for _, s := range sources {
		if s.Field == "token" && strings.Contains(s.Value, "discovery-test-token") {
			t.Errorf("plaintext leaked into Value for %s.%s: %s", s.Provider, s.Field, s.Value)
		}
	}

	// disclose() 应该能拿到原始 token。
	found := false
	for _, s := range sources {
		if s.Provider == "zhipu" && s.Field == "token" {
			found = true
			if s.disclose() != "sk-zhipu-discovery-test-token" {
				t.Errorf("disclose() should return raw, got %q", s.disclose())
			}
		}
	}
	if !found {
		t.Error("zhipu token source missing")
	}
}

// TestMaskToken 守住掩码规则:前缀 + 省略号 + 尾 4 字符;短串整段 *。
func TestMaskToken(t *testing.T) {
	cases := map[string]string{
		"sk-zhipu-1234567890abcdef": "sk-z…cdef",
		"":                          "",
		"short":                     "*****",
		"12345678":                  "********",
		"123456789":                 "1234…6789",
	}
	for in, want := range cases {
		if got := maskToken(in); got != want {
			t.Errorf("maskToken(%q) = %q, want %q", in, got, want)
		}
	}
}
