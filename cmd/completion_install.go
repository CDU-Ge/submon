package cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// completionInstallCmd 一键安装 shell 自动补全:生成补全脚本到
// ~/.local/share/submon/completion/<shell>,然后往 rc 文件追加一段
// 受标记保护的加载块(幂等:已存在则只更新脚本)。写 rc 前自动做
// 时间戳备份(<rc>.submon-bak-<ts>),卸载时还原现场。
//
//	zsh:  脚本自身 source 即可(compdef 已由 compinit 提供)
//	bash: 用 bash-completion V2 脚本,直接 source
//	fish: 生成到 vendor.completions.d 即被 fish 自动加载,不改 rc
var completionInstallCmd = &cobra.Command{
	Use:   "completion-install",
	Short: "自动安装 shell 补全(写入 rc,带备份)",
	Long: "检测当前 shell($SHELL),生成补全脚本并自动写入 rc 文件:\n\n" +
		"  zsh:  追加加载块到 ~/.zshrc(需已加载 compinit;没有则一并补上)\n" +
		"  bash: 追加加载块到 ~/.bashrc\n" +
		"  fish: 脚本放入 ~/.config/fish/completions/,无需改 rc\n\n" +
		"修改 rc 前自动备份为 <rc>.submon-bak-<时间戳>。\n" +
		"重复运行幂等(只刷新脚本,不重复追加)。\n" +
		"撤销: submon completion uninstall",
	RunE: func(cmd *cobra.Command, args []string) error {
		shell := compShell
		if shell == "" {
			shell = detectShell()
		}
		if shell == "" {
			return fmt.Errorf("无法检测 shell,请用 --shell bash|zsh|fish 指定")
		}
		return installCompletion(cmd, shell)
	},
}

var (
	compShell    string
	compUninstll bool
)

func detectShell() string {
	sh := strings.ToLower(os.Getenv("SHELL"))
	for _, s := range []string{"zsh", "bash", "fish"} {
		if strings.Contains(sh, s) {
			return s
		}
	}
	return ""
}

// completion 数据目录:XDG_DATA_HOME/submon/completion(默认 ~/.local/share/...)。
func completionDir() (string, error) {
	base := os.Getenv("XDG_DATA_HOME")
	if base == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".local", "share")
	}
	return filepath.Join(base, "submon", "completion"), nil
}

func installCompletion(cmd *cobra.Command, shell string) error {
	dir, err := completionDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	var scriptPath string
	var b strings.Builder
	switch shell {
	case "zsh":
		scriptPath = filepath.Join(dir, "_submon")
		f, err := os.Create(scriptPath)
		if err != nil {
			return err
		}
		err = rootCmd.GenZshCompletion(f)
		f.Close()
		if err != nil {
			return err
		}
		b.WriteString("# submon completion (managed block)\n")
		b.WriteString("fpath+=(" + dir + ")\n")
		b.WriteString("autoload -Uz compinit\n")
		b.WriteString("if [[ -z ${_submon_compinit_loaded:-} ]]; then\n")
		b.WriteString("  compinit\n")
		b.WriteString("  _submon_compinit_loaded=1\n")
		b.WriteString("fi\n")
	case "bash":
		scriptPath = filepath.Join(dir, "submon.bash")
		f, err := os.Create(scriptPath)
		if err != nil {
			return err
		}
		err = rootCmd.GenBashCompletionV2(f, false)
		f.Close()
		if err != nil {
			return err
		}
		b.WriteString("# submon completion (managed block)\n")
		b.WriteString("source \"" + scriptPath + "\"\n")
	case "fish":
		// fish 自动扫描 completions 目录,不需要改 rc。
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		fishDir := filepath.Join(home, ".config", "fish", "completions")
		if err := os.MkdirAll(fishDir, 0o755); err != nil {
			return err
		}
		fishPath := filepath.Join(fishDir, "submon.fish")
		ff, err := os.Create(fishPath)
		if err != nil {
			return err
		}
		err = rootCmd.GenFishCompletion(ff, true)
		ff.Close()
		if err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已安装:", fishPath)
		fmt.Fprintln(cmd.OutOrStdout(), "fish 自动加载,新开 shell 即生效")
		return nil
	default:
		return fmt.Errorf("不支持的 shell %q(bash|zsh|fish)", shell)
	}

	rcPath, err := shellRCPath(shell)
	if err != nil {
		return err
	}

	if err := appendManagedBlock(rcPath, b.String()); err != nil {
		return err
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已安装补全脚本:", scriptPath)
	fmt.Fprintln(cmd.OutOrStdout(), "已写入 rc:", rcPath)
	fmt.Fprintln(cmd.OutOrStdout(), "新开 shell 或 source 后生效")
	return nil
}

// shellRCPath 返回 rc 文件路径(不存在也返回路径,首次会创建)。
func shellRCPath(shell string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	switch shell {
	case "zsh":
		return filepath.Join(home, ".zshrc"), nil
	case "bash":
		return filepath.Join(home, ".bashrc"), nil
	}
	return "", fmt.Errorf("shell %q 无 rc 文件", shell)
}

const managedMark = "# submon completion (managed block)"

// appendManagedBlock 把受标记保护的块追加到 rc;已存在时替换旧块。
// 写入前对原文件做时间戳备份。
func appendManagedBlock(rcPath, block string) error {
	existing := []byte(nil)
	if raw, err := os.ReadFile(rcPath); err == nil {
		existing = raw
	} else if !os.IsNotExist(err) {
		return err
	}

	content := string(existing)
	if strings.Contains(content, managedMark) {
		// 已有块:替换之(脚本路径可能变了)。
		updated := replaceManagedBlock(content, block)
		return writeFileWithBackup(rcPath, []byte(updated), existing)
	}
	if len(existing) > 0 && !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += "\n" + block
	return writeFileWithBackup(rcPath, []byte(content), existing)
}

// replaceManagedBlock 替换从标记行到块尾(空行边界)的旧内容。
func replaceManagedBlock(content, newBlock string) string {
	lines := strings.Split(content, "\n")
	var out []string
	inBlock := false
	for _, l := range lines {
		if l == managedMark {
			inBlock = true
			out = append(out, strings.Split(strings.TrimRight(newBlock, "\n"), "\n")...)
			continue
		}
		if inBlock {
			// 旧块以首个空行结束。
			if strings.TrimSpace(l) == "" {
				inBlock = false
			}
			continue
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

// writeFileWithBackup 原子写 rc;文件原本存在时先备份。
func writeFileWithBackup(path string, data, orig []byte) error {
	if len(orig) > 0 {
		ts := time.Now().Format("20060102-150405")
		bak := path + ".submon-bak-" + ts
		if err := os.WriteFile(bak, append(append([]byte{}, orig...), 0), 0o644); err != nil {
			return fmt.Errorf("备份失败: %w", err)
		}
		fmt.Println("已备份:", bak)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return err
	}
	return nil
}

// uninstallCompletion 删除 rc 中的托管块与补全脚本。
func uninstallCompletion(cmd *cobra.Command) error {
	shell := compShell
	if shell == "" {
		shell = detectShell()
	}
	if shell == "" {
		return fmt.Errorf("无法检测 shell,请用 --shell bash|zsh|fish 指定")
	}
	if rcPath, err := shellRCPath(shell); err == nil {
		if raw, err := os.ReadFile(rcPath); err == nil {
			if strings.Contains(string(raw), managedMark) {
				if err := writeFileWithBackup(rcPath, []byte(replaceManagedBlock(string(raw), "")), raw); err != nil {
					return err
				}
				fmt.Fprintln(cmd.OutOrStdout(), "已从 rc 移除加载块:", rcPath)
			}
		}
	}
	dir, err := completionDir()
	if err == nil {
		_ = os.RemoveAll(dir)
	}
	fmt.Fprintln(cmd.OutOrStdout(), "已删除补全脚本目录:", dir)
	return nil
}

var completionUninstallCmd = &cobra.Command{
	Use:   "completion-uninstall",
	Short: "撤销补全安装(还原 rc,带备份)",
	RunE: func(cmd *cobra.Command, args []string) error {
		return uninstallCompletion(cmd)
	},
}

func init() {
	completionInstallCmd.Flags().StringVar(&compShell, "shell", "", "指定 shell(bash|zsh|fish),默认检测 $SHELL")
	completionInstallCmd.Flags().BoolVar(&compUninstll, "uninstall", false, "撤销安装(还原 rc,带备份)")
	rootCmd.AddCommand(completionInstallCmd)
	rootCmd.AddCommand(completionUninstallCmd)
}

// 注:cobra 的内置 completion 组会吞掉自定义子命令名,上面两个命令
// 直接挂在根下,名字带 completion 前缀以免歧义。
