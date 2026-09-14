package cmd

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
)

// configCmd 生成/更新 ~/.config/submon/config.toml:把用户现有文件解码进
// 结构体(缺失字段填默认值)再重新编码。用户设过的字段保留;schema 新增的
// 字段以默认值或迁移值出现。
var configCmd = &cobra.Command{
	Use:   "config",
	Short: "生成/更新 config.toml",
	Long: "按当前配置标准合并生成 ~/.config/submon/config.toml:\n" +
		"已有配置保留,缺失字段补默认值,~/.minimax-config.json 中的 token\n" +
		"会迁移并加密;明文 token 也会转为加密。默认只显示差异,--write 写入。",
	Example: "  submon config            # 预览与现有文件的差异,不写入\n" +
		"  submon config --write    # 确认无误后实际写入\n" +
		"  submon login             # 凭据配置走 login(自动加密)",
	RunE: runConfigMigrate,
}

// runConfigMigrate 是 config 生成的唯一实现;顶层 `submon config` 与
// deprecated 兼容路径共用。
func runConfigMigrate(cmd *cobra.Command, args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	dir, err := config.Dir()
	if err != nil {
		return err
	}
	path := filepath.Join(dir, "config.toml")

	migrated := ""
	migrate := func(slot, value string) string {
		if value == "" || secret.IsEncrypted(value) {
			return value
		}
		enc, err := secret.Encrypt(value)
		if err != nil {
			return value // keep plaintext rather than failing the merge
		}
		migrated = slot
		return enc
	}
	if cfg.FindAccount("minimax", config.DefaultLabel) == nil {
		if tok := provider.ReadLegacyMinimaxToken(); tok != "" {
			cfg.UpsertAccount(config.Account{
				Provider: "minimax",
				Label:    config.DefaultLabel,
				Token:    migrate("minimax.token (from ~/.minimax-config.json)", tok),
			})
		}
	}
	// Encrypt any plaintext tokens already in config.toml.
	for i := range cfg.Accounts {
		slot := cfg.Accounts[i].Provider + "." + cfg.Accounts[i].Label + ".token"
		cfg.Accounts[i].Token = migrate(slot, cfg.Accounts[i].Token)
	}

	var buf bytes.Buffer
	enc := toml.NewEncoder(&buf)
	enc.Indent = ""
	if err := enc.Encode(cfg); err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	generated := buf.String()

	if !generateWrite {
		prev, existed := "", false
		if raw, err := os.ReadFile(path); err == nil {
			prev, existed = string(raw), true
		}
		fmt.Fprintln(cmd.OutOrStdout(), renderConfigDiff(prev, existed, generated, path, migrated))
		fmt.Fprintln(cmd.OutOrStdout(), "预览模式:加 --write 参数才会实际写入")
		return nil
	}

	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(generated), 0o600); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已写入", path)
	if migrated != "" {
		fmt.Fprintf(cmd.OutOrStdout(), "已迁移并加密: %s(原文件可以删除了)\n", migrated)
	}
	return nil
}

// generateCmd 是 deprecated 兼容入口:原名"生成各类文件",实际只有 config
// 一个子命令 — 已拍平为顶层 `submon config`,此路径保留给旧脚本/文档。
var generateCmd = &cobra.Command{
	Use:    "generate",
	Short:  "[已弃用] 同 submon config",
	Hidden: true,
}

var generateConfigCmd = &cobra.Command{
	Use:        "config",
	Short:      "[已弃用] 同 submon config",
	Deprecated: "改用 submon config(行为一致)",
	RunE:       runConfigMigrate,
}

var generateWrite bool

// renderConfigDiff shows a line-by-line +/- preview of what would change.
func renderConfigDiff(prev string, existed bool, next, path, migrated string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "目标: %s\n", path)
	if migrated != "" {
		fmt.Fprintf(&b, "将迁移并加密: %s\n", migrated)
	}
	if !existed {
		b.WriteString("现状: 文件不存在 — 将生成全新配置\n")
	} else if prev == next {
		b.WriteString("无变化\n")
		return b.String()
	}
	b.WriteString("差异(− 旧 + 新):\n")
	prevLines := maskSecrets(strings.Split(prev, "\n"))
	nextLines := maskSecrets(strings.Split(next, "\n"))
	// Simple three-zone diff: common prefix, diff middle, common suffix.
	prefix := 0
	for prefix < len(prevLines) && prefix < len(nextLines) && prevLines[prefix] == nextLines[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(prevLines)-prefix && suffix < len(nextLines)-prefix &&
		prevLines[len(prevLines)-1-suffix] == nextLines[len(nextLines)-1-suffix] {
		suffix++
	}
	for i := 0; i < prefix; i++ {
		fmt.Fprintf(&b, "  %s\n", prevLines[i])
	}
	for i := prefix; i < len(prevLines)-suffix; i++ {
		fmt.Fprintf(&b, "- %s\n", prevLines[i])
	}
	for i := prefix; i < len(nextLines)-suffix; i++ {
		fmt.Fprintf(&b, "+ %s\n", nextLines[i])
	}
	for i := len(prevLines) - suffix; i < len(prevLines); i++ {
		fmt.Fprintf(&b, "  %s\n", prevLines[i])
	}
	return b.String()
}

// maskSecrets redacts credential-bearing lines in diff previews so tokens
// never hit the terminal.
func maskSecrets(lines []string) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		trimmed := strings.TrimSpace(l)
		isSecret := false
		for _, key := range []string{"token", "app_token"} {
			if strings.HasPrefix(trimmed, key+" = ") {
				isSecret = true
				break
			}
		}
		if isSecret && trimmed != `token = ""` && trimmed != `app_token = ""` {
			out[i] = "  " + trimmed[:strings.Index(trimmed, " = ")+3] + "\"***\""
			continue
		}
		out[i] = l
	}
	return out
}

func init() {
	configCmd.Flags().BoolVar(&generateWrite, "write", false, "写入合并后的配置(默认只预览差异)")
	generateConfigCmd.Flags().BoolVar(&generateWrite, "write", false, "写入合并后的配置(默认只预览差异)")
	generateCmd.AddCommand(generateConfigCmd)
	rootCmd.AddCommand(configCmd, generateCmd)
}
