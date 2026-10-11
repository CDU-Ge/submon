package cmd

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"
)

func TestDeployStatuslineCopiesUpdatesAndPreservesUnchanged(t *testing.T) {
	path := filepath.Join(t.TempDir(), "statusline", "submon.exe")
	first := []byte("fixture renderer v1")
	if err := deployStatuslineAt(path, first); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, first) {
		t.Fatalf("missing renderer: %q %v", got, err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := deployStatuslineAt(path, first); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("unchanged renderer rewritten")
	}
	second := []byte("fixture renderer v2")
	if err := deployStatuslineAt(path, second); err != nil {
		t.Fatal(err)
	}
	got, err = os.ReadFile(path)
	if err != nil || !bytes.Equal(got, second) {
		t.Fatalf("renderer not updated: %q %v", got, err)
	}
	items, err := os.ReadDir(filepath.Dir(path))
	if err != nil || len(items) != 1 {
		t.Fatalf("temporary files left: %v %v", items, err)
	}
}
func TestStatuslineMigrationToDeployedRenderer(t *testing.T) {
	cli := filepath.Join("tools", "submon.exe")
	helper := filepath.Join("cache", "submon", "statusline", "submon.exe")
	s := map[string]any{"statusLine": map[string]any{"command": statuslineCommand(cli), "padding": 4, "refreshInterval": 45}, "other": true}
	if got := mergeStatuslineSetting(s, helper, false, cli); got != statuslineRepaired {
		t.Fatalf("migration: %s", got)
	}
	m := s["statusLine"].(map[string]any)
	if m["command"] != statuslineCommand(helper) || m["padding"] != 4 || m["refreshInterval"] != 45 || s["other"] != true {
		t.Fatalf("lost settings: %v", s)
	}
}

func TestRendererInstallKeepsRemainingFlagsWithoutRewrite(t *testing.T) {
	helper := filepath.Join("local data", "submon", "statusline", "submon.exe")
	command := statuslineCommand(helper) + " --no-color"
	settings := map[string]any{"statusLine": map[string]any{"command": command}}
	if got := mergeStatuslineSetting(settings, helper, false); got != statuslineUnchanged {
		t.Fatalf("canonical custom flags caused rewrite: %s", got)
	}
}
