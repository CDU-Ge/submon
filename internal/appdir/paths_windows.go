//go:build windows

package appdir

import (
	"os"
	"path/filepath"
)

// Windows 没有 XDG。配置放 %AppData%(Roaming,域账号跟随用户漫游),
// 缓存放 %LocalAppData%(机器本地,不该跟着漫游)。两者由
// os.UserConfigDir / os.UserCacheDir 解析;它们只在环境变量缺失时
// (极少见,如服务账号)报错,那时退回 %USERPROFILE%。
func knownDir(resolve func() (string, error), env, fallback string) (string, error) {
	if dir, err := resolve(); err == nil && dir != "" {
		return dir, nil
	}
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
	base, err := knownDir(os.UserConfigDir, "AppData", "AppData")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "submon"), nil
}

func cacheDir() (string, error) {
	base, err := knownDir(os.UserCacheDir, "LocalAppData", "AppData")
	if err != nil {
		return "", err
	}
	return filepath.Join(base, "submon"), nil
}

// dataDir:Windows 上没有 XDG data 的概念,生成物(补全脚本)与缓存同属
// 机器本地数据,放同一棵树下。
func dataDir() (string, error) { return cacheDir() }
