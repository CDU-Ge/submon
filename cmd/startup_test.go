package cmd

import (
	"github.com/spf13/cobra"
	"testing"
)

// Cobra 的公开开关决定 Windows Execute 是否运行 Explorer 进程检测。
// CLI 高频启动不应创建全系统进程快照来判断是否从 Explorer 双击启动。
func TestCLIStartupDisablesExplorerDetection(t *testing.T) {
	if cobra.MousetrapHelpText != "" {
		t.Fatal("Cobra Explorer 检测仍启用:Windows 每次启动会扫描系统进程")
	}
}
