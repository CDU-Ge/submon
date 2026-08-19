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
var authCmd = &cobra.Command{
	Use:   "auth <provider> | ls | remove <provider> | check",
	Short: "统一配置 provider 凭据 (从 stdin 读,不入 shell history)",
	Long: "为任意 provider 加密存储凭据:\n" +
		authUsage() +
		"\n\nToken 接受 stdin 输入(TTY 时无回显,管道时明文)。\n" +
		"不要用 `submon auth <provider> <token>` 命令行参数形式 — token 会进 shell history。",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return cmd.Help()
		}
		switch args[0] {
		case "ls", "list":
			return runAuthLs(cmd)
		case "remove", "rm":
			if len(args) < 2 {
				return fmt.Errorf("用法: submon auth remove <provider>")
			}
			return runAuthRemove(cmd, args[1])
		case "check":
			return runAuthCheck(cmd)
		}
		return runAuthSet(cmd, args[0])
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
func authStorageSummary(name string, cfg *config.Config) string {
	switch name {
	case "zhipu":
		return describeToken("token", cfg.Zhipu.Token) + describeBaseURL(cfg.Zhipu.BaseURL)
	case "minimax":
		return describeToken("token", cfg.MiniMax.Token)
	case "opencode":
		return describeToken("token", cfg.OpenCode.Token)
	case "kimi":
		return describeToken("token", cfg.Kimi.Token) + describeBaseURL(cfg.Kimi.BaseURL)
	case "gotify":
		return "url=" + cfg.Notify.Gotify.URL + " " + describeToken("app_token", cfg.Notify.Gotify.AppToken)
	}
	return ""
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

// authProviderState 返回一行"凭据状态"摘要。
func authProviderState(name string, cfg *config.Config) string {
	switch name {
	case "zhipu":
		return stateOf("token", cfg.Zhipu.Token) + stateOf("base_url", boolToStr(cfg.Zhipu.BaseURL != ""))
	case "minimax":
		return stateOf("token", cfg.MiniMax.Token)
	case "opencode":
		return stateOf("token", cfg.OpenCode.Token)
	case "kimi":
		return stateOf("token", cfg.Kimi.Token) + stateOf("base_url", boolToStr(cfg.Kimi.BaseURL != ""))
	case "gotify":
		return stateOf("url", cfg.Notify.Gotify.URL) + stateOf("app_token", cfg.Notify.Gotify.AppToken)
	}
	return "-"
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
	h, ok := authRegistry[name]
	if !ok {
		return fmt.Errorf("未知 provider %q", name)
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	// 用空字符串调用 Apply 会重置该 provider 的所有字段。
	values := map[string]string{}
	for _, f := range h.Fields {
		values[f.Key] = ""
	}
	if err := h.Apply(cfg, values); err != nil {
		return err
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s 凭据已删除\n", name)
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
func authSecretValues(name string, cfg *config.Config) []string {
	switch name {
	case "zhipu":
		return nonEmpty(cfg.Zhipu.Token)
	case "minimax":
		return nonEmpty(cfg.MiniMax.Token)
	case "opencode":
		return nonEmpty(cfg.OpenCode.Token)
	case "kimi":
		return nonEmpty(cfg.Kimi.Token)
	case "gotify":
		return nonEmpty(cfg.Notify.Gotify.AppToken)
	}
	return nil
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
