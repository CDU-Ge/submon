package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/spf13/cobra"
)

// encCmd exposes the secret layer for manual use: encrypt a value for any
// tool that should not store plaintext, or decrypt submon-encoded values.
var (
	encDecrypt bool
	encStdin   bool
)

var encCmd = &cobra.Command{
	Use:   "enc [value]",
	Short: "Encrypt (or decrypt) a value with the submon master key",
	Long: "Encrypt a value into enc:v1:... form using the submon master key\n" +
		"(OS keyring-backed, see `submon token`), or decrypt with -d.\n\n" +
		"With no value argument or value '-', read from stdin.",
	Args: cobra.RangeArgs(0, 1),
	RunE: func(cmd *cobra.Command, args []string) error {
		value := ""
		if len(args) == 1 && args[0] != "-" {
			value = args[0]
		} else {
			raw, err := io.ReadAll(os.Stdin)
			if err != nil {
				return err
			}
			value = strings.TrimSpace(string(raw))
		}
		if value == "" {
			return fmt.Errorf("empty input")
		}
		if encDecrypt {
			out, err := secret.Decrypt(value)
			if err != nil {
				return err
			}
			fmt.Fprintln(cmd.OutOrStdout(), out)
			return nil
		}
		out, err := secret.Encrypt(value)
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), out)
		return nil
	},
}

func init() {
	encCmd.Flags().BoolVarP(&encDecrypt, "decrypt", "d", false, "decrypt an enc:v1:... value instead")
	rootCmd.AddCommand(encCmd)
}
