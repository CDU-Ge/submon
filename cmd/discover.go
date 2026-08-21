package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
)

// discoverCmd 是 "自动发现 + 询问式导入" 的凭据入口 — 扫环境变量、legacy
// 配置文件、~/.claude/settings.json 的 env 段,把所有找到的 token / base_url
// 列出来逐条问 Y/n 是否加密存到 config.toml。
//
// 设计:不会主动打印明文 token — 每个候选项只展示来源 + 掩码(prefix +
// 尾 4 字符)。导入 = 与 submon auth <provider> 同一加密路径。
//
// `--apply` 非交互式导入全部(适合 CI / 脚本)。`--dry-run` 只列不写。
type discoverSource struct {
	Provider string
	Field    string                       // "token" / "base_url"
	Value    string                       // 已 mask 过的展示值
	Setter   func(*config.Config, string) // 加密 + 写入 cfg
	plain    string                       // 明文,只通过 disclose() 取出,从不出现在日志/输出
}

var discoverApply bool
var discoverDryRun bool

var discoverCmd = &cobra.Command{
	Use:   "discover",
	Short: "扫描 env / legacy 文件 / Claude Code settings,逐条询问是否导入",
	Long: "在以下位置扫描凭据:\n" +
		"  - 环境变量(ANTHROPIC_AUTH_TOKEN / KIMI_CODING_API_KEY /\n" +
		"    KIMI_API_KEY / MINIMAX_TOKEN / OPENCODE_API_KEY /\n" +
		"    ANTHROPIC_BASE_URL)\n" +
		"  - 旧版 ~/.minimax-config.json\n" +
		"  - ~/.claude/settings.json 的 env 段\n\n" +
		"每个候选项只展示来源 + 掩码(从不明文打印),逐条问 Y/n 是否加密导入。\n\n" +
		"  submon auth discover              交互式\n" +
		"  submon auth discover --apply      全量导入(脚本友好)\n" +
		"  submon auth discover --dry-run    只列不写",
	RunE: func(cmd *cobra.Command, args []string) error {
		return runAuthDiscover(cmd)
	},
}

func init() {
	discoverCmd.Flags().BoolVar(&discoverApply, "apply", false, "非交互式:全量导入所有候选项")
	discoverCmd.Flags().BoolVar(&discoverDryRun, "dry-run", false, "只列出候选项,不落盘")
	authCmd.AddCommand(discoverCmd)
}

// runAuthDiscover 主流程:scan → 展示 → 询问 → 加密 → save。
func runAuthDiscover(cmd *cobra.Command) error {
	sources := scanDiscoverSources()

	if len(sources) == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "未发现任何凭据 — 检查 env / ~/.minimax-config.json / ~/.claude/settings.json")
		return nil
	}

	fmt.Fprintf(cmd.OutOrStdout(), "发现 %d 个候选项:\n", len(sources))
	for i, s := range sources {
		fmt.Fprintf(cmd.OutOrStdout(), "  %d. %s.%s = %s\n", i+1, s.Provider, s.Field, s.Value)
	}

	if discoverDryRun {
		fmt.Fprintln(cmd.OutOrStdout(), "\n(dry-run:不落盘)")
		return nil
	}

	cfg, err := config.Load()
	if err != nil {
		return err
	}

	imported := 0
	for _, s := range sources {
		// 拿真实明文(已被 Setter 闭包持有 — 这里不再从 sources.Value 拿)。
		plain := s.disclose()
		if plain == "" {
			continue
		}
		if !discoverApply {
			fmt.Fprintf(cmd.OutOrStdout(), "\n导入 %s.%s (%s)? [y/N]: ", s.Provider, s.Field, s.Value)
			line, err := readDiscoverLine()
			if err != nil {
				return err
			}
			if !strings.EqualFold(strings.TrimSpace(line), "y") {
				continue
			}
		}
		s.Setter(cfg, plain)
		imported++
	}

	if imported == 0 {
		fmt.Fprintln(cmd.OutOrStdout(), "\n没有导入任何项")
		return nil
	}
	if err := config.Save(cfg); err != nil {
		return err
	}
	fmt.Fprintf(cmd.OutOrStdout(), "\n已加密并落盘 %d 项到 %s\n", imported, mustConfigPath())
	return nil
}

// readDiscoverLine 与 readAuthField 的 bufio 复用语义相同 — 走 fd 单字节
// 读,避免 term/bufio 抢 fd。管道时透明。
func readDiscoverLine() (string, error) {
	return readLineFromFD(int(os.Stdin.Fd()))
}

// mustConfigPath 仅用于输出提示,出错回落到 "config.toml"。
func mustConfigPath() string {
	p, err := config.Path()
	if err != nil {
		return "config.toml"
	}
	return p
}

// scanDiscoverSources 扫所有已知位置,把候选项收集为 discoverSource 列表。
// 明文只在闭包里短暂持有,不进日志/stderr/stdout。
func scanDiscoverSources() []discoverSource {
	var out []discoverSource
	env := os.Environ()

	for _, e := range env {
		eq := strings.IndexByte(e, '=')
		if eq < 0 {
			continue
		}
		k, v := e[:eq], e[eq+1:]
		if v == "" {
			continue
		}
		switch k {
		case "ANTHROPIC_AUTH_TOKEN":
			out = append(out, envSource("zhipu", "token", v, setZhipuToken))
		case "KIMI_CODING_API_KEY":
			out = append(out, envSource("kimi", "token", v, setKimiToken))
		case "KIMI_API_KEY":
			// 仅在 KIMI_CODING_API_KEY 没设时纳入 — 避免两个相同账号被列两次。
			if !hasEnv(env, "KIMI_CODING_API_KEY") {
				out = append(out, envSource("kimi", "token", v, setKimiToken))
			}
		case "MINIMAX_TOKEN":
			out = append(out, envSource("minimax", "token", v, setMinimaxToken))
		case "OPENCODE_API_KEY":
			out = append(out, envSource("opencode", "token", v, setOpencodeToken))
		case "ANTHROPIC_BASE_URL":
			out = append(out, envSource("zhipu", "base_url", v, setZhipuBaseURL))
		}
	}

	// ~/.minimax-config.json
	if tok := provider.ReadLegacyMinimaxToken(); tok != "" {
		// 与 MINIMAX_TOKEN 互斥:env 已有就不重复列。
		if !hasEnv(env, "MINIMAX_TOKEN") {
			out = append(out, envSource("minimax", "token (from ~/.minimax-config.json)", tok, setMinimaxToken))
		}
	}

	// ~/.claude/settings.json 的 env 段
	for _, src := range scanClaudeSettingsEnv() {
		out = append(out, src)
	}
	return out
}

// envSource 构造一个 discoverSource,plain 通过 closure 持有,展示只给掩码。
func envSource(providerName, field, plain string, setter func(*config.Config, string)) discoverSource {
	masked := maskToken(plain)
	if field == "base_url" {
		masked = plain // URL 是公开的,不算敏感
	}
	return discoverSource{
		Provider: providerName,
		Field:    field,
		Value:    masked,
		Setter:   setter,
		plain:    plain,
	}
}

// discoverSource 内部持有明文闭包 — 隐藏在文件里、不进 JSON / 序列化。
// 明文只通过 disclose() 取出,从不出现在日志/输出。

// disclose 返回明文(只在导入时使用)。返回 "" 表示用户跳过该项
// 或明文已经被加密过(从配置里捞出来的)。
func (s discoverSource) disclose() string {
	return s.plain
}

// --- Setter closures: encrypt + write back ---

func setZhipuToken(cfg *config.Config, v string) {
	if secret.IsEncrypted(v) {
		cfg.Zhipu.Token = v
		return
	}
	enc, err := secret.Encrypt(v)
	if err != nil {
		return
	}
	cfg.Zhipu.Token = enc
}

func setKimiToken(cfg *config.Config, v string) {
	if secret.IsEncrypted(v) {
		cfg.Kimi.Token = v
		return
	}
	enc, err := secret.Encrypt(v)
	if err != nil {
		return
	}
	cfg.Kimi.Token = enc
}

func setMinimaxToken(cfg *config.Config, v string) {
	if secret.IsEncrypted(v) {
		cfg.MiniMax.Token = v
		return
	}
	enc, err := secret.Encrypt(v)
	if err != nil {
		return
	}
	cfg.MiniMax.Token = enc
}

func setOpencodeToken(cfg *config.Config, v string) {
	if secret.IsEncrypted(v) {
		cfg.OpenCode.Token = v
		return
	}
	enc, err := secret.Encrypt(v)
	if err != nil {
		return
	}
	cfg.OpenCode.Token = enc
}

func setZhipuBaseURL(cfg *config.Config, v string) {
	cfg.Zhipu.BaseURL = v
}

// --- mask / env helpers ---

// maskToken 给 token 一段掩码。短串整段 * 掉避免泄漏形态。
func maskToken(s string) string {
	if len(s) <= 8 {
		return strings.Repeat("*", len(s))
	}
	return s[:4] + "…" + s[len(s)-4:]
}

// hasEnv 检查 k=v 是否在 env 列表里。用于去重(如 KIMI_CODING_API_KEY
// 和 KIMI_API_KEY 同时设置时只列前者)。
func hasEnv(env []string, key string) bool {
	prefix := key + "="
	for _, e := range env {
		if strings.HasPrefix(e, prefix) {
			return true
		}
	}
	return false
}

// scanClaudeSettingsEnv 从 ~/.claude/settings.json 抠 env 段。CC 用户
// 通常在那里挂 ANTHROPIC_AUTH_TOKEN / ANTHROPIC_BASE_URL,这些值 import
// 进来等价于 "复制到 submon config" — 方便完全脱离 shell env。
func scanClaudeSettingsEnv() []discoverSource {
	var out []discoverSource
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	raw, err := os.ReadFile(filepath.Join(home, ".claude", "settings.json"))
	if err != nil {
		return nil
	}
	var doc struct {
		Env map[string]string `json:"env"`
	}
	if json.Unmarshal(raw, &doc) != nil || doc.Env == nil {
		return nil
	}
	for k, v := range doc.Env {
		if v == "" {
			continue
		}
		switch k {
		case "ANTHROPIC_AUTH_TOKEN":
			out = append(out, envSource("zhipu", "token (from ~/.claude/settings.json env)", v, setZhipuToken))
		case "ANTHROPIC_BASE_URL":
			out = append(out, envSource("zhipu", "base_url (from ~/.claude/settings.json env)", v, setZhipuBaseURL))
		}
	}
	return out
}
