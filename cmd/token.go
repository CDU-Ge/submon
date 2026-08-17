package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
	"golang.org/x/term"
)

// tokenCmd manages provider credentials, encrypted at rest (AES-256-GCM via
// the secret package). Tokens never appear in plaintext in config.toml.
var tokenCmd = &cobra.Command{
	Use:   "token",
	Short: "Manage provider tokens (encrypted at rest)",
}

var tokenStdin bool

// tokenSlots lists settable credential slots. zhipu.base_url stays plain
// (it's not a secret).
func tokenSlots() []string { return []string{"zhipu.token", "minimax.token", "gotify.app_token"} }

func parseSlot(slot string) (get func(*config.Config) string, set func(*config.Config, string), err error) {
	switch slot {
	case "zhipu.token":
		return func(c *config.Config) string { return c.Zhipu.Token },
			func(c *config.Config, v string) { c.Zhipu.Token = v }, nil
	case "minimax.token":
		return func(c *config.Config) string { return c.MiniMax.Token },
			func(c *config.Config, v string) { c.MiniMax.Token = v }, nil
	case "gotify.app_token":
		return func(c *config.Config) string { return c.Notify.Gotify.AppToken },
			func(c *config.Config, v string) { c.Notify.Gotify.AppToken = v }, nil
	}
	return nil, nil, fmt.Errorf("unknown slot %q (valid: %s)", slot, strings.Join(tokenSlots(), ", "))
}

var tokenSetCmd = &cobra.Command{
	Use:   "set <slot> [value]",
	Short: "Encrypt and store a token",
	Long: "Encrypt a token and write it to config.toml as enc:v1:...\n\n" +
		"Slots: zhipu.token, minimax.token, gotify.app_token\n\n" +
		"With no value argument (or value '-') the token is read without echo\n" +
		"from the terminal, or from stdin when piped.",
	Args: cobra.RangeArgs(1, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		slot := args[0]
		_, set, err := parseSlot(slot)
		if err != nil {
			return err
		}
		value := ""
		if len(args) == 2 && args[1] != "-" {
			value = args[1]
		} else {
			value, err = readSecret(tokenStdin)
			if err != nil {
				return err
			}
		}
		if value == "" {
			return fmt.Errorf("empty token")
		}
		enc, err := secret.Encrypt(value)
		if err != nil {
			return fmt.Errorf("encrypt: %w", err)
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		set(cfg, enc)
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintf(cmd.OutOrStdout(), "%s stored encrypted (%s)\n", slot, maskEnc(enc))
		return nil
	},
}

var tokenGetCmd = &cobra.Command{
	Use:   "get <slot>",
	Short: "Decrypt and print a token (for scripting)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		get, _, err := parseSlot(args[0])
		if err != nil {
			return err
		}
		cfg, err := loadDecryptedConfig()
		if err != nil {
			return err
		}
		v := get(cfg)
		if v == "" {
			return fmt.Errorf("%s is not set", args[0])
		}
		fmt.Fprintln(cmd.OutOrStdout(), v)
		return nil
	},
}

var tokenLsCmd = &cobra.Command{
	Use:   "ls",
	Short: "List token slots and their storage form",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		for _, slot := range tokenSlots() {
			get, _, _ := parseSlot(slot)
			v := get(cfg)
			switch {
			case v == "":
				fmt.Fprintf(cmd.OutOrStdout(), "%-16s -\n", slot)
			case secret.IsEncrypted(v):
				fmt.Fprintf(cmd.OutOrStdout(), "%-16s %s\n", slot, maskEnc(v))
			default:
				fmt.Fprintf(cmd.OutOrStdout(), "%-16s PLAINTEXT (migrate: submon token set %s)\n", slot, slot)
			}
		}
		return nil
	},
}

var tokenUnsetCmd = &cobra.Command{
	Use:   "unset <slot>",
	Short: "Remove a stored token",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		_, set, err := parseSlot(args[0])
		if err != nil {
			return err
		}
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		set(cfg, "")
		if err := config.Save(cfg); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), args[0], "removed")
		return nil
	},
}

// loadDecryptedConfig loads config.toml and decrypts encrypted token fields
// in place (tokens resolve to plaintext for the process lifetime).
func loadDecryptedConfig() (*config.Config, error) {
	cfg, err := config.Load()
	if err != nil {
		return nil, err
	}
	if err := cfg.DecryptSecrets(); err != nil {
		return cfg, fmt.Errorf("decrypt secrets: %w", err)
	}
	return cfg, nil
}

// readSecret reads a secret: no-echo prompt when stdin is a TTY, plain read
// when piped (scripting).
func readSecret(plainStdin bool) (string, error) {
	if plainStdin || !term.IsTerminal(int(os.Stdin.Fd())) {
		raw, err := io.ReadAll(os.Stdin)
		if err != nil {
			return "", err
		}
		return strings.TrimSpace(string(raw)), nil
	}
	fmt.Fprint(os.Stderr, "token: ")
	raw, err := term.ReadPassword(int(os.Stdin.Fd()))
	fmt.Fprintln(os.Stderr)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(raw)), nil
}

// maskEnc shortens "enc:v1:nonce:ct" for display.
func maskEnc(enc string) string {
	if len(enc) > 24 {
		return enc[:16] + "…" + enc[len(enc)-6:]
	}
	return "enc:v1:…"
}

func init() {
	tokenSetCmd.Flags().BoolVar(&tokenStdin, "stdin", false, "always read from stdin, even on a TTY")
	tokenCmd.AddCommand(tokenSetCmd, tokenGetCmd, tokenLsCmd, tokenUnsetCmd)
	rootCmd.AddCommand(tokenCmd)
}
