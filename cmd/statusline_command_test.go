package cmd

import (
	"github.com/ge/subscription-monitor/internal/model"
	"path/filepath"
	"strings"
	"testing"
)

func TestStatuslineCommandShellSafe(t *testing.T) {
	got := statuslineCommand(filepath.Join("opt", "submon", "submon"))
	if !strings.HasSuffix(got, " statusline") {
		t.Fatalf("命令必须以 statusline 结尾,得到 %q", got)
	}
	if strings.Contains(got, `\`) {
		t.Errorf("命令里不许留裸反斜杠,得到 %q", got)
	}
	if spaced := statuslineCommand(filepath.Join("my tools", "submon.exe")); !strings.HasPrefix(spaced, `"`) {
		t.Errorf("含空格路径应加引号,得到 %q", spaced)
	}
}

func TestStatusPanelProviderBadge(t *testing.T) {
	if got := providerBadge(model.ProviderMinimax); !strings.Contains(got, "MINIMAX") {
		t.Fatalf("badge: %s", got)
	}
}
