package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"syscall"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// auth 是 submon 的统一凭据入口。**所有 provider 与 gotify 走同一流程,
// 没有例外**——一个注册表,同一套"读 stdin → 加密 → 落盘"语义。
//
// 为什么不用 `submon auth <provider> <token>` 命令行参数?
//
//	shell history 会原样记录,ps -ef 也能看到,违反"token 不落盘/不入日志"基线。
//	`submon auth <provider>` 单独跑、TTY 时无回显读 stdin 是唯一推荐姿势;
//	CI/脚本走 `echo "$TOKEN" | submon auth <provider> --stdin`,仍然不沾 argv。
//
// 安全等级统一:
//   - Secret=true 字段:AES-256-GCM 加密 -> config.toml 存 enc:v1:...
//   - Secret=false 字段(URL 之类):明文,但必须经过 auth 入口(不允许手编 toml)
//   - `auth ls` 永远只显示存储形态,绝不打印明文
type authHandler struct {
	Fields []authField
	// Apply 把加密后的 values 写入 cfg(由调用方保证 Secret 字段已加密)。
	// 返回的 error 直接抛给用户。
	Apply func(cfg *config.Config, values map[string]string) error
}

type authField struct {
	Key      string // "token" / "base_url" / "url" / "app_token"
	Prompt   string
	Secret   bool // true = 无回显输入 + 加密落盘
	Required bool
}

// authRegistry 是"如何为每个 provider 配置凭据"的唯一真源。
// 新增 provider 时:**provider 实现 + registry.Build + DetectFromBaseURL + authRegistry**。
// 不允许在 cmd 层写"if name == 'xxx' { ... }"分支。
var authRegistry = map[string]authHandler{
	"zhipu": {
		Fields: []authField{
			{Key: "token", Prompt: "zhipu token", Secret: true, Required: true},
			{Key: "base_url", Prompt: "zhipu base_url (回车跳过)", Secret: false, Required: false},
		},
		Apply: func(cfg *config.Config, v map[string]string) error {
			cfg.Zhipu.Token = v["token"]
			if v["base_url"] != "" {
				cfg.Zhipu.BaseURL = v["base_url"]
			}
			return nil
		},
	},
	"minimax": {
		Fields: []authField{
			{Key: "token", Prompt: "minimax token", Secret: true, Required: true},
		},
		Apply: func(cfg *config.Config, v map[string]string) error {
			cfg.MiniMax.Token = v["token"]
			return nil
		},
	},
	"opencode": {
		Fields: []authField{
			{Key: "token", Prompt: "opencode token", Secret: true, Required: true},
		},
		Apply: func(cfg *config.Config, v map[string]string) error {
			cfg.OpenCode.Token = v["token"]
			return nil
		},
	},
	"kimi": {
		Fields: []authField{
			{Key: "token", Prompt: "kimi token", Secret: true, Required: true},
			{Key: "base_url", Prompt: "kimi base_url (回车跳过)", Secret: false, Required: false},
		},
		Apply: func(cfg *config.Config, v map[string]string) error {
			cfg.Kimi.Token = v["token"]
			if v["base_url"] != "" {
				cfg.Kimi.BaseURL = v["base_url"]
			}
			return nil
		},
	},
	"gotify": {
		Fields: []authField{
			{Key: "url", Prompt: "gotify URL (例 https://gotify.example.com)", Secret: false, Required: true},
			{Key: "app_token", Prompt: "gotify app_token", Secret: true, Required: true},
		},
		Apply: func(cfg *config.Config, v map[string]string) error {
			cfg.Notify.Gotify.URL = v["url"]
			cfg.Notify.Gotify.AppToken = v["app_token"]
			return nil
		},
	},
}

// authProviderNames 列出注册表中所有 provider,保持稳定顺序(展示用)。
func authProviderNames() []string {
	return []string{"zhipu", "minimax", "opencode", "kimi", "gotify"}
}

// authCmd 是顶层 `submon auth` 入口。手写分派而不是 cobra 子命令,让
// `submon auth zhipu` 这种"省去 add 关键字"的写法可用,同时保留 ls/remove/check。
//
// 多 key 子命令:
//
//	submon auth <provider>               配置/覆盖 default Key
//	submon auth <provider> add <label>   新增/覆盖 label Key
//	submon auth <provider> remove <label> 删除指定 label Key
//	submon auth <provider> remove        删除 default Key
var authCmd = &cobra.Command{
	Use:   "auth <provider> [add <label>] | ls | remove <provider> [<label>] | check | discover",
	Short: "统一配置 provider 凭据 (从 stdin 读,不入 shell history)",
	Long: "为任意 provider 加密存储凭据:\n" +
		authUsage() +
		"\n\nToken 接受 stdin 输入(TTY 时无回显,管道时明文)。\n" +
		"不要用 `submon auth <provider> <token>` 命令行参数形式 — token 会进 shell history。\n\n" +
		"多 key:\n" +
		"  submon auth <provider> add <label>   新增/覆盖指定 label 的 Key\n" +
		"  submon auth <provider> remove <label> 删除指定 label(默认删除 default Key)\n",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		switch args[0] {
		case "ls", "list":
			return runAuthLs(cmd)
		case "check":
			return runAuthCheck(cmd)
		}
		// provider 名
		name := args[0]
		if _, ok := authRegistry[name]; !ok {
			return fmt.Errorf("未知 provider %q (可选: %s)", name, strings.Join(authProviderNames(), " | "))
		}
		if len(args) >= 2 && args[1] == "add" {
			if len(args) < 3 {
				return fmt.Errorf("用法: submon auth %s add <label>", name)
			}
			return runAuthAddLabeled(cmd, name, args[2])
		}
		if len(args) >= 2 && (args[1] == "remove" || args[1] == "rm") {
			label := "default"
			if len(args) >= 3 {
				label = args[2]
			}
			return runAuthRemoveLabeled(cmd, name, label)
		}
		return runAuthSet(cmd, name)
	},
}

func authUsage() string {
	var lines []string
	for _, name := range authProviderNames() {
		h := authRegistry[name]
		fields := make([]string, 0, len(h.Fields))
		for _, f := range h.Fields {
			if f.Required {
				fields = append(fields, f.Key)
			}
		}
		lines = append(lines, fmt.Sprintf("  %-8s %s", name, strings.Join(fields, " + ")))
	}
	return strings.Join(lines, "\n")
}

// runAuthSet 走完整流程:读全部字段 → 加密 Secret → 调 Apply → 落盘。
func runAuthSet(cmd *cobra.Command, name string) error {
	h, ok := authRegistry[name]
	if !ok {
		return fmt.Errorf("未知 provider %q (可选: %s)", name, strings.Join(authProviderNames(), " | "))
	}
	// 读取策略:
	//   TTY → 完全不走 bufio(避免 bufio 预读与 term.ReadPassword 抢 fd)。
	//         Secret 走 term.ReadPassword(无回显),非 Secret 走 syscall.Read
	//         单字节读直到 \n — 跟 token 字段不共享任何 buffer,顺序无关。
	//   pipe → 单 bufio.Reader 跨字段复用(每次 NewReader 预读会让下个字段 EOF)。
	useTerm := !authStdin && term.IsTerminal(int(os.Stdin.Fd()))
	var br *bufio.Reader
	if !useTerm {
		br = bufio.NewReader(os.Stdin)
	}
	values := map[string]string{}
	for _, f := range h.Fields {
		v, err := readAuthField(f, br, useTerm)
		if err != nil {
			return err
		}
		if f.Required && v == "" {
			return fmt.Errorf("%s 不能为空", f.Key)
		}
		values[f.Key] = v
	}
	// 对所有 Secret 字段加密(即使是空字符串也跳过,避免空 enc)。
	for _, f := range h.Fields {
		if f.Secret && values[f.Key] != "" && !secret.IsEncrypted(values[f.Key]) {
			enc, err := secret.Encrypt(values[f.Key])
			if err != nil {
				return fmt.Errorf("encrypt %s: %w", f.Key, err)
			}
			values[f.Key] = enc
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if err := h.Apply(cfg, values); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s 凭据已加密存储 (%s)\n", name, authStorageSummary(name, cfg))
	return nil
}

// authStorageSummary 给出"存了什么"的极简可视化(用户回显用),不泄漏明文。
// 多 key 模式:显示 default + 每条 label。
func authStorageSummary(name string, cfg *config.Config) string {
	if name == "gotify" {
		return "url=" + cfg.Notify.Gotify.URL + " " + describeToken("app_token", cfg.Notify.Gotify.AppToken)
	}
	keys := cfg.ProviderKeys(model.Provider(name))
	if len(keys) == 0 {
		return "no keys"
	}
	var parts []string
	for _, k := range keys {
		parts = append(parts, fmt.Sprintf("%s=%s", k.Label, describeTokenShort(k.Token)))
	}
	return strings.Join(parts, " ")
}

func describeTokenShort(v string) string {
	switch {
	case v == "":
		return "unset"
	case secret.IsEncrypted(v):
		return "enc"
	}
	return "PLAINTEXT"
}

func describeToken(name, v string) string {
	switch {
	case v == "":
		return name + "=<unset> "
	case secret.IsEncrypted(v):
		return name + "=enc(v1) "
	default:
		return name + "=PLAINTEXT(请用 submon auth 加密) "
	}
}

func describeBaseURL(v string) string {
	if v == "" {
		return "base_url=<default> "
	}
	return "base_url=" + v + " "
}

// readAuthField 按字段属性选择输入方式:
//
//	Secret + TTY → term.ReadPassword(无回显) 直读 os.Stdin
//	非 Secret + TTY → readLineFromFD(单字节读,不预读 buffer)
//	管道输入 → br.ReadString (调用方必须复用同一个,见 runAuthSet 注释)
//
// useTerm 由 runAuthSet 根据 TTY+forceStdin 一次性评估后传入,避免每次
// 重查。"Secret TTY 走 term,非 Secret TTY 走 fd" 的分工避免了 bufio
// 预读与 term.ReadPassword 抢 fd(老代码 br 为 nil 时 base_url 字段
// 直接 panic)。
func readAuthField(f authField, br *bufio.Reader, useTerm bool) (string, error) {
	if f.Secret && useTerm {
		fmt.Fprintf(os.Stderr, "%s: ", f.Prompt)
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	fmt.Fprintf(os.Stderr, "%s: ", f.Prompt)
	if useTerm {
		return readLineFromFD(int(os.Stdin.Fd()))
	}
	line, err := br.ReadString('\n')
	if errors.Is(err, io.EOF) {
		// 管道里 EOF 之前没有换行也算完整一行。
		return strings.TrimSpace(line), nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readLineFromFD 从 fd 单字节读直到 \n — 不经过 bufio,杜绝与 term.ReadPassword
// 在同一 fd 上抢 buffer。仅 TTY 模式使用。
func readLineFromFD(fd int) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := syscall.Read(fd, one)
		if n > 0 {
			if one[0] == '\n' {
				return strings.TrimRight(string(buf), "\r"), nil
			}
			buf = append(buf, one[0])
			continue
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return strings.TrimRight(string(buf), "\r"), nil
			}
			return "", err
		}
	}
}

// runAuthLs 列出每个 provider 的存储形态(明文 / 已加密 / 未设置),
// 永不打印 token 明文。配置缺失的 provider 视为"未设置"。
func runAuthLs(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, name := range authProviderNames() {
		fmt.Fprintf(cmd.OutOrStdout(), "%-10s %s\n", name, authProviderState(name, cfg))
	}
	return nil
}

// authProviderState 返回一行"凭据状态"摘要。多 key 模式:每行同时
// 显示 default 与 [[keys]] 各条 — 形如 "zhipu:token=enc(default) alice=enc"
// 看上去紧凑又能让用户一眼看出有几个 key。
func authProviderState(name string, cfg *config.Config) string {
	if name == "gotify" {
		return stateOf("url", cfg.Notify.Gotify.URL) + stateOf("app_token", cfg.Notify.Gotify.AppToken)
	}
	keys := cfg.ProviderKeys(model.Provider(name))
	if len(keys) == 0 {
		// fallback:直接看顶层,避免漏报已存在的旧格式配置。
		switch name {
		case "zhipu":
			return stateOf("token", cfg.Zhipu.Token) + stateOf("base_url", boolToStr(cfg.Zhipu.BaseURL != ""))
		case "kimi":
			return stateOf("token", cfg.Kimi.Token) + stateOf("base_url", boolToStr(cfg.Kimi.BaseURL != ""))
		case "minimax":
			return stateOf("token", cfg.MiniMax.Token)
		case "opencode":
			return stateOf("token", cfg.OpenCode.Token)
		}
		return "-"
	}
	var parts []string
	for _, k := range keys {
		st := "enc"
		if !secret.IsEncrypted(k.Token) {
			st = "PLAINTEXT"
		}
		parts = append(parts, fmt.Sprintf("%s=%s", k.Label, st))
	}
	return strings.Join(parts, " ")
}

func stateOf(name, v string) string {
	if v == "" {
		return name + "=-  "
	}
	if secret.IsEncrypted(v) {
		return name + "=enc  "
	}
	return name + "=PLAINTEXT(建议重跑 submon auth " + name + ")  "
}

func boolToStr(b bool) string {
	if b {
		return "set"
	}
	return ""
}

// runAuthRemove 清空指定 provider 的凭据字段,不影响其他 provider。
func runAuthRemove(cmd *cobra.Command, name string) error {
	return runAuthRemoveLabeled(cmd, name, "default")
}

// runAuthRemoveLabeled 删除指定 provider 下某 label 的 Key;label = "default"
// 走顶层字段;其他 label 走 [[keys]] 数组。
func runAuthRemoveLabeled(cmd *cobra.Command, name, label string) error {
	if _, ok := authRegistry[name]; !ok {
		return fmt.Errorf("未知 provider %q", name)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !removeLabeledKey(cfg, name, label) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s/%s 不存在,无需删除\n", name, label)
		return nil
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s/%s 已删除\n", name, label)
	return nil
}

// runAuthAddLabeled 新增 / 覆盖某 provider 下某 label 的 Key。
// 复用 authRegistry 字段定义(读 token + 可选 base_url),加密后写到
// 顶层(若 label == "default")或 [[keys]] 数组(其他 label)。
func runAuthAddLabeled(cmd *cobra.Command, name, label string) error {
	h, ok := authRegistry[name]
	if !ok {
		return fmt.Errorf("未知 provider %q", name)
	}
	useTerm := !authStdin && term.IsTerminal(int(os.Stdin.Fd()))
	var br *bufio.Reader
	if !useTerm {
		br = bufio.NewReader(os.Stdin)
	}
	values := map[string]string{}
	for _, f := range h.Fields {
		v, err := readAuthField(f, br, useTerm)
		if err != nil {
			return err
		}
		if f.Required && v == "" {
			return fmt.Errorf("%s 不能为空", f.Key)
		}
		values[f.Key] = v
	}
	for _, f := range h.Fields {
		if f.Secret && values[f.Key] != "" && !secret.IsEncrypted(values[f.Key]) {
			enc, err := secret.Encrypt(values[f.Key])
			if err != nil {
				return fmt.Errorf("encrypt %s: %w", f.Key, err)
			}
			values[f.Key] = enc
		}
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	addLabeledKey(cfg, name, label, values)
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s/%s 已加密存储\n", name, label)
	return nil
}

// removeLabeledKey 删除 cfg 中 (provider, label) 对应 Key。
//
//	label == "default" → 清空 provider 顶层 Token/BaseURL,但
//	不动 [[keys]] 数组里的其他条目。label != "default" → 从
//	[[keys]] 数组移除 Label 匹配的条目;若没匹配则 no-op。
//	返回是否真的删了一条。
func removeLabeledKey(cfg *config.Config, name, label string) bool {
	if label == "default" {
		changed := false
		switch name {
		case "zhipu":
			if cfg.Zhipu.Token != "" || cfg.Zhipu.BaseURL != "" {
				cfg.Zhipu.Token, cfg.Zhipu.BaseURL = "", ""
				changed = true
			}
		case "minimax":
			if cfg.MiniMax.Token != "" {
				cfg.MiniMax.Token = ""
				changed = true
			}
		case "opencode":
			if cfg.OpenCode.Token != "" {
				cfg.OpenCode.Token = ""
				changed = true
			}
		case "kimi":
			if cfg.Kimi.Token != "" || cfg.Kimi.BaseURL != "" {
				cfg.Kimi.Token, cfg.Kimi.BaseURL = "", ""
				changed = true
			}
		case "gotify":
			if cfg.Notify.Gotify.URL != "" || cfg.Notify.Gotify.AppToken != "" {
				cfg.Notify.Gotify.URL, cfg.Notify.Gotify.AppToken = "", ""
				changed = true
			}
		}
		return changed
	}
	keys := keysField(cfg, name)
	if keys == nil {
		return false
	}
	for i := range *keys {
		if (*keys)[i].Label == label {
			*keys = append((*keys)[:i], (*keys)[i+1:]...)
			return true
		}
	}
	return false
}

// addLabeledKey 在 cfg 中追加 / 覆盖 (provider, label)。
//
//	label == "default" → 走 provider 顶层(Token / 可选 BaseURL)。
//	label != "default" → 追加到 [[keys]] 数组;若 Label 已存在则覆盖。
func addLabeledKey(cfg *config.Config, name, label string, values map[string]string) {
	if label == "default" {
		applyToProviderTop(cfg, name, values)
		return
	}
	keys := keysField(cfg, name)
	if keys == nil {
		return
	}
	for i := range *keys {
		if (*keys)[i].Label == label {
			(*keys)[i].Token = values["token"]
			if v, ok := values["base_url"]; ok && v != "" {
				(*keys)[i].BaseURL = v
			}
			return
		}
	}
	*keys = append(*keys, config.ProviderKey{
		Label:   label,
		Token:   values["token"],
		BaseURL: values["base_url"],
	})
}

// applyToProviderTop 把 values 写到 provider 顶层字段。供 addLabeledKey
// (label == "default") 与 authRegistry.Apply 共同使用 — 单 key 老路径。
func applyToProviderTop(cfg *config.Config, name string, values map[string]string) {
	switch name {
	case "zhipu":
		cfg.Zhipu.Token = values["token"]
		if values["base_url"] != "" {
			cfg.Zhipu.BaseURL = values["base_url"]
		}
	case "minimax":
		cfg.MiniMax.Token = values["token"]
	case "opencode":
		cfg.OpenCode.Token = values["token"]
	case "kimi":
		cfg.Kimi.Token = values["token"]
		if values["base_url"] != "" {
			cfg.Kimi.BaseURL = values["base_url"]
		}
	case "gotify":
		cfg.Notify.Gotify.URL = values["url"]
		cfg.Notify.Gotify.AppToken = values["app_token"]
	}
}

// keysField 返回 cfg 中 provider 对应的 [[keys]] 数组指针;未知
// provider 返回 nil(调用方按"无法配置"处理)。
func keysField(cfg *config.Config, name string) *[]config.ProviderKey {
	switch name {
	case "zhipu":
		return &cfg.Zhipu.Keys
	case "minimax":
		return &cfg.MiniMax.Keys
	case "opencode":
		return &cfg.OpenCode.Keys
	case "kimi":
		return &cfg.Kimi.Keys
	}
	return nil
}

// runAuthCheck 检查每个 provider:1) 配置是否存在;2) 能解密。
// 解密失败说明 key 文件损坏或换机器,直接报。这是 systemd watchdog 用得上的体检。
func runAuthCheck(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	var problems []string
	for _, name := range authProviderNames() {
		secrets := authSecretValues(name, cfg)
		if len(secrets) == 0 {
			problems = append(problems, name+":未配置")
			continue
		}
		for _, s := range secrets {
			if !secret.IsEncrypted(s) {
				problems = append(problems, name+":明文未加密,请 submon auth "+name)
				continue
			}
			if _, err := secret.Decrypt(s); err != nil {
				problems = append(problems, name+":解密失败("+err.Error()+")")
			}
		}
	}
	if len(problems) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "✓ 所有 provider 凭据完整且可解密")
		return nil
	}
	for _, p := range problems {
		fmt.Fprintln(cmd.ErrOrStderr(), "✗", p)
	}
	return fmt.Errorf("%d 项问题", len(problems))
}

// authSecretValues 提取一个 provider 的所有 Secret 字段值,供 check 体检用。
// 多 key 模式:default + [[keys]] 全部纳入。
func authSecretValues(name string, cfg *config.Config) []string {
	if name == "gotify" {
		return nonEmpty(cfg.Notify.Gotify.AppToken)
	}
	keys := cfg.ProviderKeys(model.Provider(name))
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k.Token)
	}
	return nonEmpty(out...)
}

func nonEmpty(s ...string) []string {
	out := make([]string, 0, len(s))
	for _, v := range s {
		if v != "" {
			out = append(out, v)
		}
	}
	return out
}

var authStdin bool

func init() {
	authCmd.Flags().BoolVar(&authStdin, "stdin", false, "强制从 stdin 读取所有字段(脚本友好)")
	rootCmd.AddCommand(authCmd)
}
