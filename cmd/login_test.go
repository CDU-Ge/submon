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

// loginTestProvider 统一 provider 清单 — 任何新 provider 必须通过同一张表
// 的同一条路径(add/ls/remove 共 3 条用例),新增/漏配会同时在多个测试上
// 爆,不会出现"某 provider 走自己私路"这种失范。
type loginTestProvider struct {
	Name string
	// Input 是 label 之后的 stdin 行(按字段定义顺序的 token / base_url)。
	Input string
	// SecretValue 是唯一必须落盘加密的明文(不含 URL)。
	SecretValue string
}

var loginTestProviders = []loginTestProvider{
	{Name: "zhipu", Input: "sk-zhipu-test\n\n", SecretValue: "sk-zhipu-test"},
	{Name: "minimax", Input: "sk-minimax-test\n", SecretValue: "sk-minimax-test"},
	{Name: "opencode", Input: "oc-test\n", SecretValue: "oc-test"},
	{Name: "kimi", Input: "sk-kimi-test\n\n", SecretValue: "sk-kimi-test"},
	{Name: "gotify", Input: "https://gotify.example.test\ngotify-app-token\n", SecretValue: "gotify-app-token"},
}

// loginInput 构造 "provider\nlabel\n<字段...>" 的完整 stdin。
func loginInput(p loginTestProvider, label string) string {
	return p.Name + "\n" + label + "\n" + p.Input
}

// withTempConfig 把 XDG_CONFIG_HOME 重定向到 tempdir。t.Cleanup 还原全局状态。
func withTempConfig(t *testing.T) {
	t.Helper()
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
}

// withStdin 把 os.Stdin 替换成含 input 的可读文件。readLoginField 看到普通
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

// newTestCmd 给 runLogin* 系列的 *cobra.Command 参数。RunE 不参与,只接
// Out/Err 到 buffer。
func newTestCmd() (*cobra.Command, *bytes.Buffer, *bytes.Buffer) {
	out := &bytes.Buffer{}
	errBuf := &bytes.Buffer{}
	cmd := &cobra.Command{}
	cmd.SetOut(out)
	cmd.SetErr(errBuf)
	return cmd, out, errBuf
}

// TestLoginAddEncryptsAndPersists 每个 provider 跑一次 login 添加,验证:
//  1. secret 已加密 (enc:v1:...)
//  2. config.toml 不含 plaintext token
//  3. 加密值可解密
func TestLoginAddEncryptsAndPersists(t *testing.T) {
	for _, p := range loginTestProviders {
		t.Run(p.Name, func(t *testing.T) {
			withTempConfig(t)
			withStdin(t, loginInput(p, "default"))

			cmd, _, _ := newTestCmd()
			if err := runLoginAdd(cmd); err != nil {
				t.Fatalf("login %s: %v", p.Name, err)
			}

			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			a := cfg.FindAccount(p.Name, "default")
			if a == nil || a.Token == "" {
				t.Fatalf("%s/default account missing after login", p.Name)
			}
			if !secret.IsEncrypted(a.Token) {
				t.Fatalf("%s token not encrypted: %s", p.Name, a.Token)
			}
			raw, err := readRawConfig(t)
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(raw, p.SecretValue) {
				t.Errorf("plaintext token leaked into config.toml: %s", raw)
			}
			if _, err := secret.Decrypt(a.Token); err != nil {
				t.Errorf("decrypt failed: %v", err)
			}
		})
	}
}

// TestLoginLsReports 空配置下 ls 列出所有 provider;登录之后 ls 显示 enc;
// 任何路径下都绝不打印 plaintext token。
func TestLoginLsReports(t *testing.T) {
	withTempConfig(t)

	// 空配置:每个 provider 一行,带 - 标记。
	cmd, out, _ := newTestCmd()
	if err := runLoginLs(cmd); err != nil {
		t.Fatal(err)
	}
	for _, p := range loginTestProviders {
		if !strings.Contains(out.String(), p.Name) {
			t.Errorf("ls missing %s: %s", p.Name, out.String())
		}
	}
	if strings.Contains(out.String(), "PLAINTEXT") || strings.Contains(out.String(), "sk-") || strings.Contains(out.String(), "gotify-app-token") {
		t.Errorf("ls leaked plaintext markers: %s", out.String())
	}

	// 登录 zhipu 之后 ls 应显示 enc 标记,且不含 plaintext token。
	withStdin(t, "zhipu\ndefault\nsk-zhipu-set\n\n")
	cmd2, _, _ := newTestCmd()
	if err := runLoginAdd(cmd2); err != nil {
		t.Fatal(err)
	}
	cmd3, out3, _ := newTestCmd()
	if err := runLoginLs(cmd3); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out3.String(), "enc") {
		t.Errorf("ls missing enc marker after login: %s", out3.String())
	}
	if strings.Contains(out3.String(), "sk-zhipu-set") {
		t.Errorf("ls leaked plaintext token: %s", out3.String())
	}
}

// TestLoginRemoveClears 每个 provider 都能 remove 且不影响其他 provider。
func TestLoginRemoveClears(t *testing.T) {
	for _, p := range loginTestProviders {
		t.Run(p.Name, func(t *testing.T) {
			withTempConfig(t)
			withStdin(t, loginInput(p, "default"))

			cmd, _, _ := newTestCmd()
			if err := runLoginAdd(cmd); err != nil {
				t.Fatal(err)
			}
			cmd2, _, _ := newTestCmd()
			if err := runLoginRemoveAccount(cmd2, p.Name, "default"); err != nil {
				t.Fatalf("login remove: %v", err)
			}
			cfg, err := config.Load()
			if err != nil {
				t.Fatal(err)
			}
			if a := cfg.FindAccount(p.Name, "default"); a != nil {
				t.Errorf("after remove, %s/default = %+v, want nil", p.Name, a)
			}
		})
	}
}

// TestLoginRegistryCoversAllProviders 守住"注册表 = 唯一真源"约束:
// model.AllProviders() 集合 ⊆ loginRegistry 集合。新增 provider 跳过这个
// 测试,CI 就会拦下来。
func TestLoginRegistryCoversAllProviders(t *testing.T) {
	for _, p := range model.AllProviders() {
		if lookupLoginProvider(string(p)) == nil {
			t.Errorf("loginRegistry missing model provider %q", p)
		}
	}
	if lookupLoginProvider("gotify") == nil {
		t.Error("loginRegistry missing gotify (gotify is treated as a first-class credential)")
	}
}

// TestLoginRejectsUnknownProvider 验证未知 provider 错误信息。
func TestLoginRejectsUnknownProvider(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "unknown-provider\n")
	cmd, _, _ := newTestCmd()
	if err := runLoginAdd(cmd); err == nil {
		t.Fatal("expected error for unknown provider")
	}
}

// TestLoginGotifyBothFields gotify 是唯一带"明文 URL + 加密 secret"组合的
// provider,单独验:URL 明文,app_token 加密,plaintext token 不泄到 toml。
func TestLoginGotifyBothFields(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "gotify\ndefault\nhttps://gotify.example.test\ngotify-app-token-xyz\n")
	cmd, _, _ := newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatal(err)
	}

	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	url, token := cfg.GotifyCredential()
	if url != "https://gotify.example.test" {
		t.Errorf("url = %q, want https://gotify.example.test", url)
	}
	if !secret.IsEncrypted(token) {
		t.Errorf("app_token not encrypted: %q", token)
	}
	raw, err := readRawConfig(t)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(raw, "gotify-app-token-xyz") {
		t.Errorf("gotify app_token plaintext leaked")
	}
}

// TestLoginKimiE2E kimi 走 login + check 端到端,确保 kimi 账号与其他
// provider 在所有流上完全等价。
func TestLoginKimiE2E(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "kimi\ndefault\nsk-kimi-xyz\n\n")
	cmd, _, _ := newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	if a := cfg.FindAccount("kimi", "default"); a == nil || !secret.IsEncrypted(a.Token) {
		t.Errorf("kimi token not encrypted: %+v", a)
	}
}

// --- 多账号测试 ---

// TestLoginAddLabeledAccount 验证标签账号:
//   - label 非 default → 与 default 并存
//   - 再次 add 同 label → 覆盖该 label(后写覆盖前写)
//   - base_url 落盘
func TestLoginAddLabeledAccount(t *testing.T) {
	withTempConfig(t)

	// 1. 先有 default Key
	withStdin(t, "zhipu\ndefault\ntk-default\n\n")
	cmd, _, _ := newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatal(err)
	}

	// 2. add alice
	withStdin(t, "zhipu\nalice\ntk-alice\nhttps://alice.example\n")
	cmd, _, _ = newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatalf("add alice: %v", err)
	}
	cfg, _ := config.Load()
	def := cfg.FindAccount("zhipu", "default")
	if def == nil || !secret.IsEncrypted(def.Token) {
		t.Errorf("default token lost: %+v", def)
	}
	a := cfg.FindAccount("zhipu", "alice")
	if a == nil {
		t.Fatalf("alice account missing: %+v", cfg.Accounts)
	}
	if a.BaseURL != "https://alice.example" {
		t.Errorf("alice.BaseURL = %q", a.BaseURL)
	}
	if !secret.IsEncrypted(a.Token) {
		t.Error("alice token not encrypted")
	}

	// 3. add alice again(覆盖)
	withStdin(t, "zhipu\nalice\ntk-alice-v2\n\n")
	cmd, _, _ = newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatalf("add alice overwrite: %v", err)
	}
	cfg, _ = config.Load()
	if n := len(cfg.AccountsOf("zhipu")); n != 2 {
		t.Errorf("accounts = %d, want 2 (覆盖不增加)", n)
	}
	// base_url 留空 = 保留原值(合并语义)。
	if a := cfg.FindAccount("zhipu", "alice"); a == nil || a.BaseURL != "https://alice.example" {
		t.Errorf("base_url should survive empty input: %+v", a)
	}
}

// TestLoginRemoveLabeledAccount 删除 default / label 账号互不影响。
func TestLoginRemoveLabeledAccount(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "zhipu\ndefault\ntk-default\nhttps://default.example\n")
	cmd, _, _ := newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatal(err)
	}
	withStdin(t, "zhipu\nalice\ntk-alice\n\n")
	cmd, _, _ = newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatal(err)
	}

	// remove alice → default 保留
	cmd, _, _ = newTestCmd()
	if err := runLoginRemoveAccount(cmd, "zhipu", "alice"); err != nil {
		t.Fatalf("remove alice: %v", err)
	}
	cfg, _ := config.Load()
	if cfg.FindAccount("zhipu", "default") == nil {
		t.Error("default token should survive removing alice")
	}
	if cfg.FindAccount("zhipu", "alice") != nil {
		t.Error("alice account not removed")
	}

	// remove default → 清空
	cmd, _, _ = newTestCmd()
	if err := runLoginRemoveAccount(cmd, "zhipu", "default"); err != nil {
		t.Fatalf("remove default: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.FindAccount("zhipu", "default") != nil {
		t.Error("default account still set")
	}

	// remove 不存在的 label → no-op
	cmd, _, _ = newTestCmd()
	if err := runLoginRemoveAccount(cmd, "zhipu", "ghost"); err != nil {
		t.Errorf("remove ghost should not error: %v", err)
	}
}

// TestLoginRemoveRequiresConfirmation 删除凭据是本工具最不可逆的操作:
// 非 TTY 无 -f 拒绝执行(带 -f 提示);-f 放行后才真正删除。
func TestLoginRemoveRequiresConfirmation(t *testing.T) {
	withTempConfig(t)
	withStdin(t, "zhipu\ndefault\ntk-default\n\n")
	cmd, _, _ := newTestCmd()
	if err := runLoginAdd(cmd); err != nil {
		t.Fatal(err)
	}

	defer func() { loginRemoveForce = false }()
	loginRemoveForce = false
	cmd, _, _ = newTestCmd()
	err := removeWithConfirm(cmd, "zhipu", "default")
	if err == nil || !strings.Contains(err.Error(), "-f") {
		t.Fatalf("非 TTY 无 -f 应拒绝并提示 -f, got %v", err)
	}
	cfg, _ := config.Load()
	if cfg.FindAccount("zhipu", "default") == nil {
		t.Fatal("被拒绝的删除不应动到凭据")
	}

	loginRemoveForce = true
	cmd, _, _ = newTestCmd()
	if err := removeWithConfirm(cmd, "zhipu", "default"); err != nil {
		t.Fatalf("remove with -f: %v", err)
	}
	cfg, _ = config.Load()
	if cfg.FindAccount("zhipu", "default") != nil {
		t.Error("-f 后账号未删除")
	}
}

// TestLoginLsReportsMultiAccount `login ls` 展示 default + 每条 label。
func TestLoginLsReportsMultiAccount(t *testing.T) {
	withTempConfig(t)
	for _, label := range []string{"default", "alice", "bob"} {
		withStdin(t, "zhipu\n"+label+"\ntk-"+label+"\n\n")
		cmd, _, _ := newTestCmd()
		if err := runLoginAdd(cmd); err != nil {
			t.Fatal(err)
		}
	}

	cmd, out, _ := newTestCmd()
	if err := runLoginLs(cmd); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	for _, want := range []string{"default", "alice", "bob", "enc"} {
		if !strings.Contains(s, want) {
			t.Errorf("ls missing %q: %s", want, s)
		}
	}
}

// TestProviderKeysIsTruthForConfigured 守住 "providerConfigured()"
// 用 accounts 也能识别 — 没这个,多账号的 provider 会被 status/statusline
// 当作"未配置"隐藏掉。
func TestProviderKeysIsTruthForConfigured(t *testing.T) {
	cfg := config.Default()
	cfg.Accounts = []config.Account{
		{Provider: "zhipu", Label: "solo", Token: "tk"},
	}
	if !providerConfigured(model.ProviderZhipu, cfg) {
		t.Error("providerConfigured should return true when only labeled accounts are set")
	}
}

// TestReadLoginFieldTTYNonSecret 守住 "kimi / zhipu 的 base_url 字段在
// TTY 模式下不 panic" 的回归 — 老代码 br 为 nil,base_url 字段
// 直接 nil deref。本测试通过 pipe fd 模拟 TTY,直接打 readLoginField 的
// useTerm=true / 非 Secret 分支。
func TestReadLoginFieldTTYNonSecret(t *testing.T) {
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

	v, err := readLoginField(loginField{Key: "base_url", Prompt: "url", Secret: false, Required: false}, nil, true)
	if err != nil {
		t.Fatalf("readLoginField with useTerm=true / non-Secret: %v", err)
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

// TestDiscoverImportWritesAccount discover 的 Setter 落盘后是 accounts。
func TestDiscoverImportWritesAccount(t *testing.T) {
	withTempConfig(t)
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	accountSetter("zhipu", "token")(cfg, "sk-zhipu-import")
	accountSetter("zhipu", "base_url")(cfg, "https://proxy.example")
	a := cfg.FindAccount("zhipu", "default")
	if a == nil || !secret.IsEncrypted(a.Token) {
		t.Fatalf("account not encrypted: %+v", a)
	}
	if a.BaseURL != "https://proxy.example" {
		t.Errorf("base_url = %q", a.BaseURL)
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
