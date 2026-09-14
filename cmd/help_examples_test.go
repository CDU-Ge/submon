package cmd

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

// TestCommandsHaveExamples 验证面向用户的子命令都挂了 Example 字段,
// clig.deck 的 help.md 要求帮助以示例开头。每个子命令至少给 1 行。
func TestCommandsHaveExamples(t *testing.T) {
	wantExample := []*cobra.Command{
		statusCmd, watchCmd, loginCmd, installCmd, checkCmd,
		versionCmd, notifyCmd, cacheCmd, configCmd,
	}
	for _, c := range wantExample {
		if c == nil {
			continue
		}
		if strings.TrimSpace(c.Example) == "" {
			t.Errorf("cmd %q 缺 Example 字段(clig.deck 要求示例开头)", c.Name())
		}
	}
}

// TestVersionCommandExposed 验证 version 子命令存在(Subcommands
// 常用别名之一;很多 CI 流水线靠 `app --version`/`app version` 读版本)。
func TestVersionCommandExposed(t *testing.T) {
	if versionCmd == nil {
		t.Fatal("versionCmd 应为非 nil")
	}
	if versionCmd.Use != "version" {
		t.Fatalf("versionCmd.Use 应为 \"version\",得到 %q", versionCmd.Use)
	}
}

// TestRootCmdHasNoColorFlag 验证 --no-color 是 rootCmd 上的 persistent flag,
// 这样所有子命令都能拿到。
func TestRootCmdHasNoColorFlag(t *testing.T) {
	f := rootCmd.PersistentFlags().Lookup("no-color")
	if f == nil {
		t.Fatal("rootCmd 缺 --no-color persistent flag")
	}
}

// TestDiscoverAttachedToLogin 验证 discover 挂在 login 下(凭据入口统一),
// 且旧 auth 子命令已不存在。
func TestDiscoverAttachedToLogin(t *testing.T) {
	found := false
	for _, c := range loginCmd.Commands() {
		if c.Name() == "discover" {
			found = true
		}
	}
	if !found {
		t.Error("discover 应为 login 的子命令")
	}
	for _, c := range rootCmd.Commands() {
		if c.Name() == "auth" {
			t.Error("auth 命令应已移除,统一为 login")
		}
		if c.Name() == "token" {
			t.Error("token 命令应已移除(login 是其替代)")
		}
	}
}

// TestNotifyResetHasLegacyArmAlias 验证 `notify reset` 同时接受旧
// `notify arm` 别名,但标 Deprecated — 改术语但不破坏老脚本。
func TestNotifyResetHasLegacyArmAlias(t *testing.T) {
	if notifyResetCmd == nil {
		t.Fatal("notifyResetCmd 应为非 nil")
	}
	found := false
	for _, a := range notifyResetCmd.Aliases {
		if a == "arm" {
			found = true
		}
	}
	if !found {
		t.Errorf("notifyResetCmd 应保留 arm 别名,得到 %v", notifyResetCmd.Aliases)
	}
	if notifyResetCmd.Deprecated == "" {
		t.Errorf("notifyResetCmd 的 arm 别名应标 Deprecated 提示用户改用 reset")
	}
}
