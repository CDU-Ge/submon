// Package appdir resolves submon's per-user directories on every supported
// platform — the single source of truth for "where does config.toml /
// cache.json / secret.key live".
//
//	POSIX    XDG Base Directory: $XDG_CONFIG_HOME (default ~/.config),
//	         $XDG_CACHE_HOME (default ~/.cache), $XDG_DATA_HOME
//	         (default ~/.local/share) — see paths_unix.go
//	Windows  %AppData%\submon (config, roams) and %LocalAppData%\submon
//	         (cache + generated data, machine-local) — see paths_windows.go
//
// 每个函数返回的是 submon 自己的目录(不含文件名),目录不存在时不报错 —
// 建目录是调用方的事。
package appdir

// Config returns the directory holding config.toml and secret.key.
func Config() (string, error) { return configDir() }

// Cache returns the directory holding cache.json.
func Cache() (string, error) { return cacheDir() }

// Data returns the directory for generated artifacts (shell completions).
func Data() (string, error) { return dataDir() }
