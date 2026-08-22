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
	Short: "用 submon 主密钥加密/解密任意值",
	Long: "用 submon 主密钥把任意值加密为 enc:v1:... 形式,-d 解密。\n\n" +
		"不带参数或参数为 '-' 时从 stdin 读取。",
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
			return fmt.Errorf("输入为空")
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
	encCmd.Flags().BoolVarP(&encDecrypt, "decrypt", "d", false, "改为解密 enc:v1:... 值")
	rootCmd.AddCommand(encCmd)
}
