package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// login 是 submon 的凭据入口。**所有 provider 与 gotify 走同一流程,
// 没有例外**——一个注册表,同一套"交互选择 → 读 stdin → 加密 → 落盘
// [[accounts]]"语义。
//
// 为什么不用 `submon login <token>` 这种命令行参数形式?
//
//	shell history 会原样记录,ps -ef 也能看到,违反"token 不落盘/不入日志"基线。
//	`submon login` 单独跑、TTY 时无回显读 stdin 是唯一推荐姿势;
//	CI/脚本走 `printf 'zhipu\nalice\n%s\n\n' "$TOKEN" | submon login`,不沾 argv。
//
// 安全等级统一:
//   - Secret=true 字段:AES-256-GCM 加密 -> config.toml 存 enc:v1:...
//   - Secret=false 字段(URL 之类):明文,但必须经过 login 入口
//   - `login ls` 永远只显示存储形态,绝不打印明文
type loginProvider struct {
	Name      string
	TokenName string // 展示用字段名("token" / "app_token")
	Fields    []loginField
}

type loginField struct {
	Key      string // "token" / "base_url"
	Prompt   string
	Secret   bool // true = 无回显输入 + 加密落盘
	Required bool
}

// loginRegistry 是"每个 provider 如何配置凭据"的唯一真源。
// 新增 provider 时:**provider 实现 + registry.Build + DetectFromBaseURL + loginRegistry**。
// 不允许在 cmd 层写"if name == 'xxx' { ... }"分支。
var loginRegistry = []loginProvider{
	{
		Name: "zhipu", TokenName: "token",
		Fields: []loginField{
			{Key: "token", Prompt: "zhipu token", Secret: true, Required: true},
			{Key: "base_url", Prompt: "zhipu base_url(可选,回车使用默认)", Secret: false},
		},
	},
	{
		Name: "minimax", TokenName: "token",
		Fields: []loginField{
			{Key: "token", Prompt: "minimax token", Secret: true, Required: true},
		},
	},
	{
		Name: "opencode", TokenName: "token",
		Fields: []loginField{
			{Key: "token", Prompt: "opencode token", Secret: true, Required: true},
		},
	},
	{
		Name: "kimi", TokenName: "token",
		Fields: []loginField{
			{Key: "token", Prompt: "kimi token", Secret: true, Required: true},
			{Key: "base_url", Prompt: "kimi base_url(可选,回车使用默认)", Secret: false},
		},
	},
	{
		Name: "gotify", TokenName: "app_token",
		Fields: []loginField{
			{Key: "base_url", Prompt: "gotify 服务地址(例 https://gotify.example.com)", Secret: false, Required: true},
			{Key: "token", Prompt: "gotify app_token", Secret: true, Required: true},
		},
	},
}

// lookupLoginProvider 按名找注册项;未知返回 nil。
func lookupLoginProvider(name string) *loginProvider {
	for i := range loginRegistry {
		if loginRegistry[i].Name == name {
			return &loginRegistry[i]
		}
	}
	return nil
}

func loginProviderNames() []string {
	names := make([]string, 0, len(loginRegistry))
	for i := range loginRegistry {
		names = append(names, loginRegistry[i].Name)
	}
	return names
}

var loginCmd = &cobra.Command{
	Use:   "login [ls|list] | remove [provider [label]] | discover",
	Short: "登录账号(默认添加/更新凭据)",
	Long: "登录并加密保存一个订阅账号(provider + 标签 + token)。\n" +
		"无参数运行 = 添加账号,依次询问 provider / 标签 / token:\n\n" +
		"  submon login                交互式添加(zhipu / minimax / opencode / kimi / gotify)\n" +
		"  submon login ls             列出所有账号与存储形态(永不打印明文)\n" +
		"  submon login remove         交互式选择并删除账号\n" +
		"  submon login remove zhipu alice  直接删除指定账号(需 -f 或 TTY 确认)\n" +
		"  submon login discover       扫 env / CC settings,询问式导入\n\n" +
		"Token 接受 stdin 输入(TTY 时无回显,管道时逐行读)。\n" +
		"不要用 `submon login <token>` 命令行参数形式 — token 会进 shell history。",
	Example: "  submon login                                # 交互式添加账号\n" +
		"  printf 'zhipu\\nalice\\n%s\\n\\n' \"$TOKEN\" | submon login  # 脚本:provider/label/token/base_url\n" +
		"  submon login ls                             # 看账号列表(只显示存储形态)\n" +
		"  submon login remove zhipu alice             # 删除指定账号\n" +
		"  submon login remove -f zhipu default        # 脚本删除\n" +
		"  submon login discover                       # 从 env / CC settings 导入",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) == 0 {
			return runLoginAdd(cmd)
		}
		switch args[0] {
		case "ls", "list":
			return runLoginLs(cmd)
		case "remove", "rm":
			return runLoginRemove(cmd, args[1:])
		}
		return fmt.Errorf("未知子命令 %q\n  用法: submon login | login ls | login remove [provider [label]] | login discover\n  看帮助: submon login --help", args[0])
	},
}

// runLoginAdd 是默认路径:选 provider → 标签 → 逐字段读入 → 加密 → 写
// [[accounts]]。字段定义全部来自 loginRegistry。
func runLoginAdd(cmd *cobra.Command) error {
	useTerm := !loginStdin && term.IsTerminal(int(os.Stdin.Fd()))
	var br *bufio.Reader
	if !useTerm {
		br = bufio.NewReader(os.Stdin)
	}

	name, err := chooseLoginProvider(cmd, br, useTerm)
	if err != nil {
		return err
	}
	spec := lookupLoginProvider(name)

	label, err := promptLoginLabel(br, useTerm)
	if err != nil {
		return err
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if a := cfg.FindAccount(name, label); a != nil {
		fmt.Fprintf(cmd.ErrOrStderr(), "%s/%s 已存在(token=%s),新输入将覆盖\n",
			name, label, describeTokenShort(a.Token))
	}

	values := map[string]string{}
	for _, f := range spec.Fields {
		v, err := readLoginField(f, br, useTerm)
		if err != nil {
			return err
		}
		if f.Required && v == "" {
			return fmt.Errorf("%s 不能为空 — 重新运行 `submon login` 输入有效值", f.Key)
		}
		values[f.Key] = v
	}
	for _, f := range spec.Fields {
		if f.Secret && values[f.Key] != "" && !secret.IsEncrypted(values[f.Key]) {
			enc, err := secret.Encrypt(values[f.Key])
			if err != nil {
				return fmt.Errorf("加密 %s 失败: %w\n  常见原因: 无法写入 %s(检查权限)", f.Key, err, secretKeyHint())
			}
			values[f.Key] = enc
		}
	}

	cfg.UpsertAccount(config.Account{
		Provider: name,
		Label:    label,
		Token:    values["token"],
		BaseURL:  values["base_url"],
	})
	if err := config.Save(cfg); err != nil {
		return err
	}
	summary := fmt.Sprintf("%s=%s", spec.TokenName, describeTokenShort(values["token"]))
	if values["base_url"] != "" {
		summary += " base_url=" + values["base_url"]
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s/%s 凭据已加密存储(%s)\n", name, label, summary)
	return nil
}

// chooseLoginProvider 选 provider:交互时打印菜单,接受序号或名字;
// 管道时读一行(同样接受序号或名字)。
func chooseLoginProvider(cmd *cobra.Command, br *bufio.Reader, useTerm bool) (string, error) {
	if useTerm {
		fmt.Fprintln(cmd.ErrOrStderr(), "选择 provider:")
		for i, p := range loginRegistry {
			fmt.Fprintf(cmd.ErrOrStderr(), "  %d) %s\n", i+1, p.Name)
		}
	}
	fmt.Fprint(cmd.ErrOrStderr(), "provider: ")
	line, err := readLoginLine(br, useTerm)
	if err != nil {
		return "", err
	}
	line = strings.TrimSpace(line)
	if line == "" {
		return "", fmt.Errorf("provider 不能为空 — 可选: %s", strings.Join(loginProviderNames(), " | "))
	}
	if n, err := strconv.Atoi(line); err == nil {
		if n < 1 || n > len(loginRegistry) {
			return "", fmt.Errorf("序号 %d 超出范围(1-%d)", n, len(loginRegistry))
		}
		return loginRegistry[n-1].Name, nil
	}
	if lookupLoginProvider(line) == nil {
		return "", fmt.Errorf("不认识 provider %q\n  可选: %s\n  看用法: submon login --help",
			line, strings.Join(loginProviderNames(), " | "))
	}
	return line, nil
}

// promptLoginLabel 询问标签;回车 = default。
func promptLoginLabel(br *bufio.Reader, useTerm bool) (string, error) {
	fmt.Fprint(os.Stderr, "标签(回车 = default,多账号时用于区分): ")
	line, err := readLoginLine(br, useTerm)
	if err != nil {
		return "", err
	}
	label := strings.TrimSpace(line)
	if label == "" {
		return config.DefaultLabel, nil
	}
	if strings.ContainsAny(label, "/ \t") {
		return "", fmt.Errorf("标签不能包含空格或 %q — 例: alice、公司", "/")
	}
	return label, nil
}

// readLoginLine 读一行:TTY 走 fd 单字节读(不预读 buffer),管道走共享
// bufio.Reader(每次 NewReader 预读会让下个字段 EOF)。
func readLoginLine(br *bufio.Reader, useTerm bool) (string, error) {
	if useTerm {
		return readLineRaw(os.Stdin)
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

// readLoginField 按字段属性选择输入方式:
//
//	Secret + TTY → term.ReadPassword(无回显) 直读 os.Stdin
//	非 Secret + TTY → readLineRaw(单字节读,不预读 buffer)
//	管道输入 → br.ReadString (调用方必须复用同一个,见 runLoginAdd 注释)
func readLoginField(f loginField, br *bufio.Reader, useTerm bool) (string, error) {
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
		return readLineRaw(os.Stdin)
	}
	line, err := br.ReadString('\n')
	if errors.Is(err, io.EOF) {
		return strings.TrimSpace(line), nil
	}
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(line), nil
}

// readLineRaw 从 f 单字节读直到 \n — 不经过 bufio,杜绝与 term.ReadPassword
// 在同一 fd 上抢 buffer。仅 TTY 模式使用(调用方传 os.Stdin)。
// 用 *os.File 而不是裸 fd:os.File.Read 本身无缓冲,syscall.Read 在各平台
// 的 fd 类型不同(Windows 是 HANDLE),包一层才能跨平台。
func readLineRaw(f *os.File) (string, error) {
	var buf []byte
	one := make([]byte, 1)
	for {
		n, err := f.Read(one)
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

// runLoginLs 列出所有账号与存储形态(明文 / 已加密 / 未设置),
// 永不打印 token 明文。没有账号的 provider 显示 "-"。
func runLoginLs(cmd *cobra.Command) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	for _, spec := range loginRegistry {
		accounts := cfg.AccountsOf(spec.Name)
		if len(accounts) == 0 {
			fmt.Fprintf(cmd.OutOrStdout(), "%-10s -\n", spec.Name)
			continue
		}
		for _, a := range accounts {
			line := fmt.Sprintf("%-10s %-10s %s=%s", spec.Name, a.Label, spec.TokenName, describeTokenShort(a.Token))
			if a.BaseURL != "" {
				line += "  base_url=" + a.BaseURL
			}
			fmt.Fprintln(cmd.OutOrStdout(), line)
		}
	}
	return nil
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

// runLoginRemove 删除账号。参数: [provider [label]]。
//
//	无参数 → 交互式列出账号选择(非 TTY 报错提示直接给 provider/label)
//	只有 provider → 删该 provider 的 default
//	provider + label → 删指定账号
//
// 删除前确认:TTY 走 y/N,非 TTY 要求 -f(与 cache clear 同一危险等级)。
func runLoginRemove(cmd *cobra.Command, args []string) error {
	name, label := "", ""
	switch len(args) {
	case 0:
		var err error
		name, label, err = pickLoginAccount(cmd)
		if err != nil {
			return err
		}
	case 1:
		name, label = args[0], config.DefaultLabel
	case 2:
		name, label = args[0], args[1]
	default:
		return fmt.Errorf("用法: submon login remove [provider [label]]\n  例: submon login remove zhipu alice")
	}
	if lookupLoginProvider(name) == nil {
		return fmt.Errorf("不认识 provider %q\n  可选: %s", name, strings.Join(loginProviderNames(), " | "))
	}
	return removeWithConfirm(cmd, name, label)
}

// pickLoginAccount 列出所有账号让用户选序号或输 "provider/label"/"provider"。
func pickLoginAccount(cmd *cobra.Command) (string, string, error) {
	cfg, err := config.Load()
	if err != nil {
		return "", "", err
	}
	var list []config.Account
	for _, spec := range loginRegistry {
		list = append(list, cfg.AccountsOf(spec.Name)...)
	}
	if len(list) == 0 {
		return "", "", fmt.Errorf("没有已配置的账号可删除 — 先运行 `submon login` 添加")
	}
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return "", "", fmt.Errorf("非交互式环境请直接指定账号:\n  submon login remove <provider> [label]\n  例: submon login remove zhipu default")
	}
	for i, a := range list {
		fmt.Fprintf(cmd.ErrOrStderr(), "  %d) %s/%s\n", i+1, a.Provider, a.Label)
	}
	fmt.Fprint(cmd.ErrOrStderr(), "要删除哪个账号?序号或 provider/label: ")
	line, err := readLineRaw(os.Stdin)
	if err != nil {
		return "", "", err
	}
	line = strings.TrimSpace(line)
	if n, err := strconv.Atoi(line); err == nil {
		if n < 1 || n > len(list) {
			return "", "", fmt.Errorf("序号 %d 超出范围(1-%d)", n, len(list))
		}
		return list[n-1].Provider, list[n-1].Label, nil
	}
	if name, label, ok := strings.Cut(line, "/"); ok {
		if name == "" {
			return "", "", fmt.Errorf("无法解析 %q — 例: zhipu/alice", line)
		}
		if label == "" {
			label = config.DefaultLabel
		}
		return name, label, nil
	}
	if line != "" {
		return line, config.DefaultLabel, nil
	}
	return "", "", fmt.Errorf("未选择账号,已取消")
}

// removeWithConfirm 删除前的确认层。凭据删除后无法找回(token 需重新
// 获取),是本工具最不可逆的操作:TTY 走 y/N 确认,非 TTY 要求 -f 显式
// 放行 — 与 `cache clear` 同一危险等级。内部测试直接调 runLoginRemoveAccount,
// 不经过此层。
func removeWithConfirm(cmd *cobra.Command, name, label string) error {
	if !loginRemoveForce {
		if !term.IsTerminal(int(os.Stdin.Fd())) {
			return fmt.Errorf("非交互式环境拒绝执行删除(避免脚本误删):\n  强制删除: submon login remove %s %s -f", name, label)
		}
		fmt.Fprintf(cmd.ErrOrStderr(), "将删除 %s/%s 已存凭据(删除后需重新获取并配置)。确认? [y/N]: ", name, label)
		line, err := readLineRaw(os.Stdin)
		if err != nil {
			return err
		}
		if !strings.EqualFold(strings.TrimSpace(line), "y") {
			fmt.Fprintln(cmd.OutOrStdout(), "已取消")
			return nil
		}
	}
	return runLoginRemoveAccount(cmd, name, label)
}

// runLoginRemoveAccount 执行删除:从 [[accounts]] 移除 (provider,label)。
func runLoginRemoveAccount(cmd *cobra.Command, name, label string) error {
	if lookupLoginProvider(name) == nil {
		return fmt.Errorf("不认识 provider %q\n  可选: %s", name, strings.Join(loginProviderNames(), " | "))
	}
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	if !cfg.RemoveAccount(name, label) {
		fmt.Fprintf(cmd.OutOrStdout(), "%s/%s 不存在,无需删除\n", name, label)
		return nil
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "%s/%s 已删除\n", name, label)
	return nil
}

var loginStdin bool
var loginRemoveForce bool

func init() {
	loginCmd.Flags().BoolVar(&loginStdin, "stdin", false, "强制从 stdin 逐行读取(脚本友好)")
	loginCmd.Flags().BoolVarP(&loginRemoveForce, "force", "f", false, "跳过删除确认(脚本友好)")
	rootCmd.AddCommand(loginCmd)
}
