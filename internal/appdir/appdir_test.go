package appdir

import (
	"path/filepath"
	"runtime"
	"testing"
)

// TestDirNamesAreSubmon 守住三个目录都以 "submon" 结尾 —— 帮助文案、文档和
// 备份脚本都按这个名字找目录,平台实现换了也不能改。
func TestDirNamesAreSubmon(t *testing.T) {
	for name, resolve := range map[string]func() (string, error){
		"Config": Config, "Cache": Cache, "Data": Data,
	} {
		got, err := resolve()
		if err != nil {
			t.Fatalf("%s() 报错: %v", name, err)
		}
		if filepath.Base(got) != "submon" {
			t.Errorf("%s() = %q, 期望以 submon 结尾", name, got)
		}
	}
}

// TestXDGOverrides 守住 POSIX 上的 XDG 约定:环境变量必须优先,否则
// 已有用户(以及所有把 XDG_* 指到临时目录的测试)会写到别处去。
func TestXDGOverrides(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows 走 %AppData%/%LocalAppData%,不认 XDG")
	}
	base := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "cfg"))
	t.Setenv("XDG_CACHE_HOME", filepath.Join(base, "cache"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))

	cases := []struct {
		name    string
		resolve func() (string, error)
		dir     string
	}{
		{"Config", Config, "cfg"},
		{"Cache", Cache, "cache"},
		{"Data", Data, "data"},
	}
	for _, c := range cases {
		got, err := c.resolve()
		if err != nil {
			t.Fatalf("%s() 报错: %v", c.name, err)
		}
		want := filepath.Join(base, c.dir, "submon")
		if got != want {
			t.Errorf("%s() = %q, 期望 %q", c.name, got, want)
		}
	}
}
