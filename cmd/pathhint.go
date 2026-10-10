package cmd

import (
	"path/filepath"

	"github.com/ge/subscription-monitor/internal/appdir"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/secret"
)

// 帮助文案与错误提示里的路径都从这里取:写死 ~/.config/submon 在 Windows 上
// 会指到错的地方(那边是 %AppData%\submon),用户照着找会扑空。
// 路径解析失败(极端环境)时退回 POSIX 惯例写法,至少不是空白。

// configPathHint 返回 config.toml 的真实路径。
func configPathHint() string {
	if p, err := config.Path(); err == nil {
		return p
	}
	return "~/.config/submon/config.toml"
}

// cachePathHint 返回 cache.json 的真实路径。
func cachePathHint() string {
	dir, err := appdir.Cache()
	if err != nil {
		return "~/.cache/submon/cache.json"
	}
	return filepath.Join(dir, "cache.json")
}

// secretKeyHint 返回加密主密钥文件的真实路径。
func secretKeyHint() string { return secret.KeyPath() }
