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

// generateCmd writes a config.toml that reflects the current schema while
// preserving existing values: decode the user's file into the struct (which
// fills defaults for missing fields), then re-encode. A field the user set
// stays; a field the schema added since their file was written appears with
// its default or a migrated value.
var generateCmd = &cobra.Command{
	Use:   "generate",
	Short: "Generate project files",
}

var generateConfigCmd = &cobra.Command{
	Use:   "config",
	Short: "Create/update config.toml without losing existing settings",
	Long: "Merge-generate ~/.config/submon/config.toml against the current schema:\n" +
		"existing values are preserved, missing fields are filled with defaults,\n" +
		"and a MiniMax token is migrated from ~/.minimax-config.json if present\n" +
		"and no token is configured yet. Diffs are shown; --write applies them.",
	RunE: func(cmd *cobra.Command, args []string) error {
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
		if cfg.MiniMax.Token == "" {
			if tok := provider.ReadLegacyMinimaxToken(); tok != "" {
				cfg.MiniMax.Token = migrate("minimax.token (from ~/.minimax-config.json)", tok)
			}
		}
		// Encrypt any plaintext tokens already in config.toml.
		cfg.Zhipu.Token = migrate("zhipu.token", cfg.Zhipu.Token)
		cfg.MiniMax.Token = migrate("minimax.token", cfg.MiniMax.Token)
		cfg.Notify.Gotify.AppToken = migrate("gotify.app_token", cfg.Notify.Gotify.AppToken)

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
			fmt.Fprintln(cmd.OutOrStdout(), "dry-run: re-run with --write to apply")
			return nil
		}

		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, []byte(generated), 0o600); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "wrote", path)
		if migrated != "" {
			fmt.Fprintf(cmd.OutOrStdout(), "migrated minimax token from %s (you may delete that file)\n", migrated)
		}
		return nil
	},
}

var generateWrite bool

// renderConfigDiff shows a line-by-line +/- preview of what would change.
func renderConfigDiff(prev string, existed bool, next, path, migrated string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "target: %s\n", path)
	if migrated != "" {
		fmt.Fprintf(&b, "would migrate minimax token from %s\n", migrated)
	}
	if !existed {
		b.WriteString("current: <missing> — a fresh config will be created\n")
	} else if prev == next {
		b.WriteString("no changes\n")
		return b.String()
	}
	b.WriteString("changes (− old, + new):\n")
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
	generateConfigCmd.Flags().BoolVar(&generateWrite, "write", false, "write the merged config (default: dry-run with diff)")
	generateCmd.AddCommand(generateConfigCmd)
	rootCmd.AddCommand(generateCmd)
}
