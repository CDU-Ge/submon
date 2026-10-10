//go:build !windows

package appdir

import (
	"os"
	"path/filepath"
)

// xdgBase 解析一个 XDG 基础目录:环境变量优先,否则 $HOME/<fallback>。
// XDG 是 POSIX 惯例,macOS 上也沿用(历史行为,迁移会丢用户配置)。
func xdgBase(env, fallback string) (string, error) {
	if v := os.Getenv(env); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, fallback), nil
}

func configDir() (string, error) {
	base, err := xdgBase("XDG_CONFIG_HOME", ".config")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "submon"), nil
}

func cacheDir() (string, error) {
	base, err := xdgBase("XDG_CACHE_HOME", ".cache")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "submon"), nil
}

func dataDir() (string, error) {
	base, err := xdgBase("XDG_DATA_HOME", filepath.Join(".local", "share"))
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "submon"), nil
}
