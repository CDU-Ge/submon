// Package config loads submon configuration from the platform config dir
// (POSIX ~/.config/submon/config.toml, Windows %AppData%\submon\config.toml;
// see internal/appdir) with environment-variable overrides.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/ge/subscription-monitor/internal/appdir"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/secret"
	"github.com/pelletier/go-toml/v2"
)

// DefaultLabel 是"默认账号"的标签。空 label 在读取/写入时统一规范化为它。
const DefaultLabel = "default"

// Account 是一套已配置的凭据 —— 账号是唯一的凭据单位。
//
//	Provider 账号所属服务("zhipu" / "minimax" / "opencode" / "kimi" / "gotify")。
//	Label    区分同 provider 的多个账号("alice" / "公司"),空 = default。
//	Token    凭据密文或明文;gotify 时是 app_token。
//	BaseURL  可选覆盖:provider 基址(智谱/Kimi),gotify 时是服务地址。
//
// 同一 provider 允许多个 Account(多账号主备、key 轮换)。ProviderKeys 是
// 读取入口:按 provider 过滤、规范化 label、剔除空 token。
type Account struct {
	Provider string `toml:"provider"`
	Label    string `toml:"label,omitempty"`
	Token    string `toml:"token"`
	BaseURL  string `toml:"base_url,omitempty"`
}

type Config struct {
	// Accounts 是全部凭据账号(gotify 也在内,作为通知凭据账号)。
	Accounts []Account `toml:"accounts,omitempty"`
	Cache    struct {
		// TTLSeconds is how long a fetched snapshot is served from cache.
		// Default 300 (5 min).
		TTLSeconds int `toml:"ttl_seconds"`
		// Path defaults to the platform cache dir (POSIX
		// ~/.cache/submon/cache.json, Windows %LocalAppData%\submon\cache.json).
		Path string `toml:"path"`
	} `toml:"cache"`
	Notify struct {
		// WarnBelowPct / CritBelowPct 是剩余百分比阈值(用于状态栏染色)。
		WarnBelowPct float64 `toml:"warn_below_pct"`
		CritBelowPct float64 `toml:"crit_below_pct"`
		// RemindBefore: 窗口重置前多久开始评估"催用"提醒(如 "1h"、"30m"),0 = 关闭。
		RemindBefore string `toml:"remind_before"`
		// WasteNotifyPct: 按当前使用速率预测,重置时预计剩余(浪费)≥ 此值
		// → 普通提醒(🔴 之外的常规推送)。
		WasteNotifyPct float64 `toml:"waste_notify_pct"`
		// WasteUrgentPct: 预计浪费 ≥ 此值 → 紧急提醒(高优先级推送 + 🔴 标题)。
		WasteUrgentPct float64 `toml:"waste_urgent_pct"`
		Gotify         struct {
			// URL / AppToken 已迁到 [[accounts]](provider = "gotify"),
			// 这里只保留推送行为配置。
			// Priority 是 Gotify 优先级(1-10),默认 5;紧急提醒自动 +3(上限 10)。
			Priority int `toml:"priority"`
		} `toml:"gotify"`
	} `toml:"notify"`
	Statusline struct {
		// Layout: 状态栏排版方案("a"|"b"|"c"),默认 "a"。详见 README。
		Layout string `toml:"layout"`
	} `toml:"statusline"`
}

// Default returns built-in defaults.
func Default() *Config {
	c := &Config{}
	c.Cache.TTLSeconds = 300
	c.Notify.WarnBelowPct = 20
	c.Notify.CritBelowPct = 5
	c.Notify.RemindBefore = "1h"
	// 预计浪费 ≥30% 普通提醒,≥60% 紧急提醒。
	c.Notify.WasteNotifyPct = 30
	c.Notify.WasteUrgentPct = 60
	c.Notify.Gotify.Priority = 5
	c.Statusline.Layout = "a"
	return c
}

// RemindBeforeDuration 解析 remind_before 配置为时长;0 或非法值 = 关闭催用提醒。
func (c *Config) RemindBeforeDuration() time.Duration {
	if c.Notify.RemindBefore == "" {
		return time.Hour
	}
	d, err := time.ParseDuration(c.Notify.RemindBefore)
	if err != nil || d <= 0 {
		return 0
	}
	return d
}

// Dir returns the config directory(~/.config/submon;Windows 下是
// %AppData%\submon)。解析规则统一在 internal/appdir。
func Dir() (string, error) {
	return appdir.Config()
}

// Load reads config.toml if present and applies env overrides. A missing file
// is not an error.
//
// 旧格式([zhipu] token + [[zhipu.keys]])在内存中迁移为 [[accounts]];
// 任何一次 Save(login / config --write)都会把文件落成新格式。
func Load() (*Config, error) {
	cfg := Default()
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")
	if raw, err := os.ReadFile(path); err == nil {
		if err := toml.Unmarshal(raw, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		var legacy legacyConfig
		if err := toml.Unmarshal(raw, &legacy); err == nil {
			cfg.migrateLegacy(&legacy)
		}
	}
	if cfg.Cache.TTLSeconds <= 0 {
		cfg.Cache.TTLSeconds = 300
	}
	if cfg.Notify.WarnBelowPct <= 0 {
		cfg.Notify.WarnBelowPct = 20
	}
	if cfg.Notify.CritBelowPct <= 0 {
		cfg.Notify.CritBelowPct = 5
	}
	if cfg.Notify.Gotify.Priority <= 0 {
		cfg.Notify.Gotify.Priority = 5
	}
	if cfg.Notify.RemindBefore == "" {
		cfg.Notify.RemindBefore = "1h"
	}
	if cfg.Notify.WasteNotifyPct <= 0 {
		cfg.Notify.WasteNotifyPct = 30
	}
	if cfg.Notify.WasteUrgentPct <= 0 {
		cfg.Notify.WasteUrgentPct = 60
	}
	if cfg.Statusline.Layout == "" {
		cfg.Statusline.Layout = "a"
	}
	// 环境变量覆盖,便于临时切换方案预览:SUBMON_LAYOUT=a|b|c
	if v := os.Getenv("SUBMON_LAYOUT"); v != "" {
		cfg.Statusline.Layout = v
	}
	return cfg, nil
}

// CachePath resolves the cache file location(默认 ~/.cache/submon/cache.json;
// Windows 下是 %LocalAppData%\submon\cache.json)。
func (c *Config) CachePath() (string, error) {
	if c.Cache.Path != "" {
		return c.Cache.Path, nil
	}
	dir, err := appdir.Cache()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "cache.json"), nil
}

// Path returns the config file location.
func Path() (string, error) {
	dir, err := Dir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.toml"), nil
}

// Save writes the config back to disk atomically (0600: it holds encrypted
// secrets, but keep it locked down regardless). 总是按新 schema 编码。
func Save(c *Config) error {
	path, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	var buf bytes.Buffer
	if err := toml.NewEncoder(&buf).Encode(c); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".submon-config-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err := tmp.Write(buf.Bytes()); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}

// DecryptSecrets decrypts encrypted account tokens in place. Plaintext values
// pass through. Returns the first decryption error encountered (fields stay
// as-is when decryption fails).
func (c *Config) DecryptSecrets() error {
	var firstErr error
	for i := range c.Accounts {
		v := &c.Accounts[i].Token
		if !secret.IsEncrypted(*v) {
			continue
		}
		pt, err := secret.Decrypt(*v)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		*v = pt
	}
	return firstErr
}

// --- 账号访问 API ---

// ProviderKeys 返回 provider 的全部可用账号(规范化后的副本)。规则:
//
//  1. 只返回该 provider 的账号;空 label:第一个未命名账号为 "default",
//     其余自动编号 "key-N"(跳过已占用的 label)。
//  2. 空 token 的账号跳过(占位/base_url-only 账号不参与取数)。
//  3. 同 label 出现多次:后面的覆盖前面(原位替换,保持第一次出现的顺序)。
//
// 调用方拿到的是副本,修改不影响 Config。
func (c *Config) ProviderKeys(p model.Provider) []Account {
	return normalizeAccounts(c.Accounts, string(p))
}

// AccountsOf 返回任意 provider 名的规范化账号列表(覆盖 gotify 这种
// 非配额 provider)。
func (c *Config) AccountsOf(provider string) []Account {
	return normalizeAccounts(c.Accounts, provider)
}

// FindAccount 返回 provider 下 label 匹配的原始账号;空 label 等同 default。
// 找不到返回 nil。返回指针可直接修改(写回后 Save)。
func (c *Config) FindAccount(provider, label string) *Account {
	label = normalizeLabel(label)
	for i := range c.Accounts {
		a := &c.Accounts[i]
		if a.Provider == provider && normalizeLabel(a.Label) == label {
			return a
		}
	}
	return nil
}

// UpsertAccount 插入或合并一个账号;空 label 规范化为 default。同
// (provider, label) 已存在时只覆盖非空字段(Token / BaseURL)——
// 这样 discover 可以单独导入 base_url 而不清掉已有 token。
func (c *Config) UpsertAccount(a Account) {
	a.Label = normalizeLabel(a.Label)
	for i := range c.Accounts {
		cur := &c.Accounts[i]
		if cur.Provider == a.Provider && normalizeLabel(cur.Label) == a.Label {
			if a.Token != "" {
				cur.Token = a.Token
			}
			if a.BaseURL != "" {
				cur.BaseURL = a.BaseURL
			}
			return
		}
	}
	c.Accounts = append(c.Accounts, a)
}

// RemoveAccount 删除 (provider, label) 账号,返回是否真的删了一条。
// label 空/省略等同 default。
func (c *Config) RemoveAccount(provider, label string) bool {
	label = normalizeLabel(label)
	for i := range c.Accounts {
		a := c.Accounts[i]
		if a.Provider == provider && normalizeLabel(a.Label) == label {
			c.Accounts = append(c.Accounts[:i], c.Accounts[i+1:]...)
			return true
		}
	}
	return false
}

// GotifyCredential 返回 gotify 通知凭据(服务地址 + app_token)。未配置时
// 返回空串;cfg 需是 DecryptSecrets 后的产物。
func (c *Config) GotifyCredential() (url, token string) {
	if a := c.FindAccount("gotify", DefaultLabel); a != nil {
		return a.BaseURL, a.Token
	}
	return "", ""
}

// normalizeLabel: 空 label → default。
func normalizeLabel(label string) string {
	if label == "" {
		return DefaultLabel
	}
	return label
}

// normalizeAccounts 过滤 provider、规范化 label、跳过空 token、同 label
// 后写覆盖前写。
func normalizeAccounts(all []Account, provider string) []Account {
	out := make([]Account, 0, len(all))
	used := map[string]bool{}
	for _, a := range all {
		if a.Provider != provider || a.Token == "" {
			continue
		}
		label := a.Label
		if label == "" {
			// 第一个未命名账号即 default(单账号手写配置的直觉),
			// 其余自动编号 key-N。
			if !used[DefaultLabel] {
				label = DefaultLabel
			} else {
				label = nextAutoLabel(used)
			}
		}
		used[label] = true
		a.Label = label
		replaced := false
		for i := range out {
			if out[i].Label == label {
				out[i] = a
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, a)
		}
	}
	return out
}

// nextAutoLabel 找一个未用过的 "key-N" 标签,N 从 1 递增。
func nextAutoLabel(used map[string]bool) string {
	for n := 1; ; n++ {
		l := fmt.Sprintf("key-%d", n)
		if !used[l] {
			used[l] = true
			return l
		}
	}
}

// --- 旧格式迁移 ---

// legacyConfig 只用于解码旧 schema(顶层 provider 表 + [[keys]] 数组 +
// [notify.gotify] 凭据)。迁移完成后不再使用。
type legacyConfig struct {
	Zhipu struct {
		BaseURL string      `toml:"base_url"`
		Token   string      `toml:"token"`
		Keys    []legacyKey `toml:"keys"`
	} `toml:"zhipu"`
	MiniMax struct {
		Token string      `toml:"token"`
		Keys  []legacyKey `toml:"keys"`
	} `toml:"minimax"`
	OpenCode struct {
		Token string      `toml:"token"`
		Keys  []legacyKey `toml:"keys"`
	} `toml:"opencode"`
	Kimi struct {
		BaseURL string      `toml:"base_url"`
		Token   string      `toml:"token"`
		Keys    []legacyKey `toml:"keys"`
	} `toml:"kimi"`
	Notify struct {
		Gotify struct {
			URL      string `toml:"url"`
			AppToken string `toml:"app_token"`
		} `toml:"gotify"`
	} `toml:"notify"`
}

type legacyKey struct {
	Label   string `toml:"label"`
	Token   string `toml:"token"`
	BaseURL string `toml:"base_url"`
}

// migrateLegacy 把旧格式凭据折叠进 Accounts。已存在的 (provider,label)
// 以新格式为准(不覆盖);空 label 分配 key-N(跳过显式 label)。
func (c *Config) migrateLegacy(l *legacyConfig) {
	migrateOne := func(provider, baseURL, token string, keys []legacyKey) {
		if token != "" || baseURL != "" {
			c.addMigrated(Account{Provider: provider, Label: DefaultLabel, Token: token, BaseURL: baseURL})
		}
		used := map[string]bool{}
		for _, k := range keys {
			if k.Label != "" {
				used[k.Label] = true
			}
		}
		for _, k := range keys {
			if k.Token == "" {
				continue
			}
			label := k.Label
			if label == "" {
				label = nextAutoLabel(used)
			}
			c.addMigrated(Account{Provider: provider, Label: label, Token: k.Token, BaseURL: k.BaseURL})
		}
	}
	migrateOne("zhipu", l.Zhipu.BaseURL, l.Zhipu.Token, l.Zhipu.Keys)
	migrateOne("minimax", "", l.MiniMax.Token, l.MiniMax.Keys)
	migrateOne("opencode", "", l.OpenCode.Token, l.OpenCode.Keys)
	migrateOne("kimi", l.Kimi.BaseURL, l.Kimi.Token, l.Kimi.Keys)
	if l.Notify.Gotify.URL != "" || l.Notify.Gotify.AppToken != "" {
		c.addMigrated(Account{
			Provider: "gotify",
			Label:    DefaultLabel,
			Token:    l.Notify.Gotify.AppToken,
			BaseURL:  l.Notify.Gotify.URL,
		})
	}
}

// addMigrated 仅当 (provider,label) 尚不存在时追加 —— 新格式优先。
func (c *Config) addMigrated(a Account) {
	if c.FindAccount(a.Provider, a.Label) != nil {
		return
	}
	c.Accounts = append(c.Accounts, a)
}
