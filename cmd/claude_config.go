package cmd

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"
)

// installClaudeCmd 自动配置 Claude Code 的 statusLine:合并写入
// ~/.claude/settings.json(保留其他字段),statusLine 指向当前二进制的
// 绝对路径,refreshInterval 采用默认最优值 30(空闲时倒计时也保持新鲜,
// statusline 只读缓存,30s 重跑一次零成本)。
var installClaudeCmd = &cobra.Command{
	Use:   "claude",
	Short: "把 Claude Code 状态栏指向 submon",
	Long: "把 Claude Code 的 statusLine 指向 submon:\n\n" +
		"  submon install claude          写入 ~/.claude/settings.json\n" +
		"  submon install claude --print  只预览将写入的 JSON\n\n" +
		"已有配置合并保留(不覆盖其他字段);refreshInterval=30 让空闲会话\n" +
		"的状态栏倒计时也保持新鲜 —— statusline 只读缓存,高频调用无额外开销。",
	RunE: func(cmd *cobra.Command, args []string) error {
		bin, err := os.Executable()
		if err != nil {
			return err
		}
		bin, err = filepath.Abs(bin)
		if err != nil {
			return err
		}

		path, err := claudeSettingsPath()
		if err != nil {
			return err
		}

		// 合并:读现有 JSON(任意未知字段原样保留),只动 statusLine。
		// 这里是用户明确要求"指向 submon",所以 force=true(自己的脚本被
		// 顶掉属于预期),但 padding/refreshInterval 之类的调整仍保留。
		settings := map[string]any{}
		if raw, err := os.ReadFile(path); err == nil {
			if err := json.Unmarshal(raw, &settings); err != nil {
				return fmt.Errorf("解析 %s: %w(手动修复后重试)", path, err)
			}
		}
		action := mergeStatuslineSetting(settings, bin, true)
		out, err := json.MarshalIndent(settings, "", "  ")
		if err != nil {
			return err
		}

		if claudePrint {
			fmt.Fprintln(cmd.OutOrStdout(), string(out))
			return nil
		}
		if action == statuslineUnchanged {
			// 内容没变就别写:写一次会让 Claude Code 重载并重跑一遍命令。
			fmt.Fprintln(cmd.OutOrStdout(), "Claude Code 状态栏:已是最新,未改动", path)
			return nil
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(path, append(out, '\n'), 0o600); err != nil {
			return err
		}
		fmt.Fprintln(cmd.OutOrStdout(), "已写入:", path)
		if action == statuslineRepaired {
			// Claude Code 会监听 settings.json 自动重载,不用重启会话。
			fmt.Fprintln(cmd.OutOrStdout(), "statusLine 命令已修正(Windows 反斜杠 → 正斜杠);Claude Code 会自动重载")
		} else {
			fmt.Fprintln(cmd.OutOrStdout(), "重启 Claude Code 会话后生效")
		}
		return nil
	},
}

var claudePrint bool

// statuslinePath 返回要写进命令行的二进制路径(Git Bash 安全形式:
// 正斜杠;含空格时加引号,两种 shell 都按需切词)。
func statuslinePath(bin string) string {
	p := filepath.ToSlash(bin)
	if strings.ContainsAny(p, " \t") {
		p = `"` + p + `"`
	}
	return p
}

// statuslineCommand 生成写进 Claude Code settings.json 的 statusLine 命令。
//
// Claude Code 在 Windows 上通过 **Git Bash** 执行这条命令(没装 Git Bash
// 才退回 PowerShell),而 Git Bash 会把未加引号的路径里的反斜杠当转义符
// 吃掉:"E:\Tools\submon.exe statusline" 到脚本运行器手里变成
// "E:Toolssubmon.exe statusline" —— 命令静默失败,状态栏一片空白,用户
// 看不到任何报错(官方文档 Troubleshooting → Status line not appearing
// 明写这一条)。
func statuslineCommand(bin string) string {
	return statuslinePath(bin) + " statusline"
}

// statuslineCommandTail 判断命令是否指向本二进制,是则返回路径之后的参数
// 部分(调用方用来"只换路径、保留用户自己加的 --provider 等参数")。
//
// 比较前抹掉引号、把反斜杠归一到正斜杠 —— 旧版 install 写出的
// "E:\Tools\submon.exe statusline" 必须被认出来,否则永远修不了它。
func statuslineCommandTail(cmd, bin string) (string, bool) {
	c := strings.ReplaceAll(strings.TrimSpace(cmd), `"`, "")
	c = strings.ReplaceAll(c, `\`, `/`)
	want := filepath.ToSlash(bin)
	switch {
	case c == want:
		return "", true
	case strings.HasPrefix(c, want+" "):
		return strings.TrimSpace(strings.TrimPrefix(c, want)), true
	default:
		return "", false
	}
}

// statuslineSettingAction 说明 mergeStatuslineSetting 对现有配置做了什么。
type statuslineSettingAction string

const (
	statuslineInstalled statuslineSettingAction = "installed" // 之前没有 → 写入完整配置
	statuslineRepaired  statuslineSettingAction = "repaired"  // 我们自己写的、命令已过时 → 只改 command
	statuslineUnchanged statuslineSettingAction = "unchanged" // 已是对的
	statuslineForeign   statuslineSettingAction = "foreign"   // 别人写的命令 → 不碰
)

// mergeStatuslineSetting 把 statusLine 合并/修复进 settings(就地修改),并
// 返回做了什么,由调用方决定落盘与提示。
//
// 修复场景是本仓库自己挖的坑:早期 install 写的是 Windows 原生反斜杠路径
// ("E:\Tools\submon.exe statusline"),而 Claude Code 在 Windows 上经 Git Bash
// 执行,反斜杠会被当转义符吃掉 → 命令静默失败、状态栏空白(官方文档
// Troubleshooting 明写这条)。所以"已注册就跳过"是错的,必须能自我纠正:
//
//   - 我们自己写的条目、命令不是当前规范形式 → 只改 command,用户对
//     padding / refreshInterval / hideVimModeIndicator 的调整原样保留;
//   - 别人写的条目:install 路径(force=false)一律不碰;`install claude`
//     (force=true)是用户明确要求指向 submon,才覆盖 command 并补齐字段。
func mergeStatuslineSetting(settings map[string]any, bin string, force bool) statuslineSettingAction {
	want := statuslineCommand(bin)
	existing, ok := settings["statusLine"]
	if !ok || existing == nil {
		settings["statusLine"] = map[string]any{
			"type":            "command",
			"command":         want,
			"padding":         0,
			"refreshInterval": 30,
		}
		return statuslineInstalled
	}
	m, ok := existing.(map[string]any)
	if !ok {
		return statuslineForeign // 形状不认识,别乱动
	}
	cur, _ := m["command"].(string)
	tail, ours := statuslineCommandTail(cur, bin)
	switch {
	case !ours:
		if !force {
			return statuslineForeign
		}
		m["type"] = "command"
		m["command"] = want
		if _, ok := m["padding"]; !ok {
			m["padding"] = 0
		}
		if _, ok := m["refreshInterval"]; !ok {
			m["refreshInterval"] = 30
		}
		return statuslineRepaired
	case cur != want:
		// 只换路径:用户自己加的 --provider / --show-provider 等参数保留
		// (他们的调用方式没变,坏的只是路径的写法)。
		if tail != "" {
			m["command"] = statuslinePath(bin) + " " + tail
		} else {
			m["command"] = want
		}
		return statuslineRepaired
	default:
		return statuslineUnchanged
	}
}

// statuslineIsOurs 判断已有命令是否指向本二进制。
func statuslineIsOurs(cmd, bin string) bool {
	_, ok := statuslineCommandTail(cmd, bin)
	return ok
}

// statuslineActionText 把合并结果翻译成给用户看的一句话(--print 预览用)。
func statuslineActionText(a statuslineSettingAction) string {
	switch a {
	case statuslineInstalled:
		return "新增"
	case statuslineRepaired:
		return "修正命令"
	case statuslineUnchanged:
		return "已是最新"
	default:
		return "保留现有自定义命令,不改动"
	}
}

func init() {
	installClaudeCmd.Flags().BoolVar(&claudePrint, "print", false, "只预览将写入的 JSON,不落盘")
	installCmd.AddCommand(installClaudeCmd)
}
