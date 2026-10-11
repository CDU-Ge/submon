package cmd

import (
	"strings"
	"testing"
)

// TestMergeStatuslineSetting 守住 install 的自我修复能力。
//
// 背景(官方文档 Troubleshooting → "Status line not appearing"):Claude Code
// 在 Windows 上经 Git Bash 执行 statusLine 命令,未加引号的路径里的反斜杠
// 会被当转义符吃掉 —— 早期 install 写出的 "E:\Tools\submon.exe statusline"
// 因此静默失败、状态栏空白。所以"见到 statusLine 就跳过"是错的:必须认出
// 自己写的条目并就地纠正,同时仍然不碰用户自己的命令。
func TestMergeStatuslineSetting(t *testing.T) {
	bin := "/opt/submon/submon"
	want := statuslineCommand(bin)
	// 旧写法:同一路径的正斜杠形式换成反斜杠(Windows 上就是真实事故形态)。
	stale := strings.ReplaceAll(want, "/", `\`)

	t.Run("全新安装写入规范命令", func(t *testing.T) {
		s := map[string]any{}
		if got := mergeStatuslineSetting(s, bin, false); got != statuslineInstalled {
			t.Fatalf("action = %q, want installed", got)
		}
		m := s["statusLine"].(map[string]any)
		if m["command"] != want || m["type"] != "command" || m["refreshInterval"] != 30 {
			t.Errorf("写入的条目不对: %+v", m)
		}
	})

	t.Run("自己写坏的条目就地修复", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{
			"type": "command", "command": stale, "padding": 2,
		}}
		if got := mergeStatuslineSetting(s, bin, false); got != statuslineRepaired {
			t.Fatalf("action = %q, want repaired", got)
		}
		m := s["statusLine"].(map[string]any)
		if m["command"] != want {
			t.Errorf("command = %q, want %q", m["command"], want)
		}
		// 用户调过的字段必须原样保留。
		if m["padding"] != 2 {
			t.Errorf("padding 被改动了: %v", m["padding"])
		}
	})

	t.Run("自己写的但保留用户加的参数", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{
			"type": "command", "command": strings.ReplaceAll(want, "/", `\`) + " --provider zhipu",
		}}
		if got := mergeStatuslineSetting(s, bin, false); got != statuslineRepaired {
			t.Fatalf("action = %q, want repaired", got)
		}
		m := s["statusLine"].(map[string]any)
		if m["command"] != want+" --provider zhipu" {
			t.Errorf("command = %q, want %q", m["command"], want+" --provider zhipu")
		}
	})

	t.Run("已经正确则不动", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{"type": "command", "command": want}}
		if got := mergeStatuslineSetting(s, bin, false); got != statuslineUnchanged {
			t.Fatalf("action = %q, want unchanged", got)
		}
	})

	t.Run("别人的命令不碰", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{
			"type": "command", "command": "~/.claude/statusline.sh", "padding": 4,
		}}
		if got := mergeStatuslineSetting(s, bin, false); got != statuslineForeign {
			t.Fatalf("action = %q, want foreign", got)
		}
		m := s["statusLine"].(map[string]any)
		if m["command"] != "~/.claude/statusline.sh" {
			t.Errorf("别人的命令被改了: %q", m["command"])
		}
	})

	t.Run("install claude 明确要求才覆盖别人的命令", func(t *testing.T) {
		s := map[string]any{"statusLine": map[string]any{
			"type": "command", "command": "~/.claude/statusline.sh", "padding": 4,
		}}
		if got := mergeStatuslineSetting(s, bin, true); got != statuslineRepaired {
			t.Fatalf("action = %q, want repaired", got)
		}
		m := s["statusLine"].(map[string]any)
		if m["command"] != want {
			t.Errorf("command = %q, want %q", m["command"], want)
		}
		if m["padding"] != 4 {
			t.Errorf("padding 应保留: %v", m["padding"])
		}
		if m["refreshInterval"] != 30 {
			t.Errorf("缺失字段应补齐: %v", m["refreshInterval"])
		}
	})

	t.Run("形状不认识就不动", func(t *testing.T) {
		s := map[string]any{"statusLine": "weird"}
		if got := mergeStatuslineSetting(s, bin, false); got != statuslineForeign {
			t.Fatalf("action = %q, want foreign", got)
		}
		if s["statusLine"] != "weird" {
			t.Errorf("值被改了: %v", s["statusLine"])
		}
	})
}

// TestStatuslineCommandTail 守住"认得出自己的命令"的边界:多一个空格、
// 加了参数、引号包裹、反斜杠写法都要认;其它二进制 / 非同族命令都不认。
func TestStatuslineCommandTail(t *testing.T) {
	bin := "/opt/submon/submon"
	for _, c := range []struct {
		cmd  string
		ours bool
		tail string
	}{
		{"/opt/submon/submon statusline", true, "statusline"},
		{`\opt\submon\submon statusline`, true, "statusline"},
		{`"/opt/submon/submon" statusline --provider zhipu`, true, "statusline --provider zhipu"},
		{"/opt/submon/submon", true, ""},
		{"/opt/submon/submon-other statusline", false, ""},
		{"~/.claude/statusline.sh", false, ""},
		{"", false, ""},
	} {
		tail, ours := statuslineCommandTail(c.cmd, bin)
		if ours != c.ours || tail != c.tail {
			t.Errorf("statuslineCommandTail(%q) = (%q, %v), want (%q, %v)", c.cmd, tail, ours, c.tail, c.ours)
		}
	}
}
