// Package config loads submon configuration from ~/.config/submon/config.toml
// with environment-variable overrides.
package config

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/secret"
)

// ProviderKey 是同一 provider 下的"一个账号/一套凭据"。
//
//	Label    用户可读标签,用于面板区分("alice" / "公司" / "default")。
//	         空 Label 在 ProviderKeys 阶段会被规范化为 "default" 或 "key-N"。
//	Token    与 provider 顶层 Token 同语义,加密或明文。
//	BaseURL  可选覆盖;空时回落到 provider 顶层 BaseURL,再回落到内置默认。
//
//	同一 provider 允许多个 Key — 同一账号多 key(轮换)、多账号(主备)
//	都属此情形。ProviderKeys 是读取入口,合并顶层"default" 槽位与 [[keys]]。
type ProviderKey struct {
	Label   string `toml:"label"`
	Token   string `toml:"token"`
	BaseURL string `toml:"base_url,omitempty"`
}

type Config struct {
	Zhipu struct {
		BaseURL string        `toml:"base_url"`
		Token   string        `toml:"token"`
		Keys    []ProviderKey `toml:"keys,omitempty"`
	} `toml:"zhipu"`
	MiniMax struct {
		Token string        `toml:"token"`
		Keys  []ProviderKey `toml:"keys,omitempty"`
	} `toml:"minimax"`
	OpenCode struct {
		Token string        `toml:"token"`
		Keys  []ProviderKey `toml:"keys,omitempty"`
	} `toml:"opencode"`
	Kimi struct {
		BaseURL string        `toml:"base_url"`
		Token   string        `toml:"token"`
		Keys    []ProviderKey `toml:"keys,omitempty"`
	} `toml:"kimi"`
	Cache struct {
		// TTLSeconds is how long a fetched snapshot is served from cache.
		// Default 300 (5 min).
		TTLSeconds int `toml:"ttl_seconds"`
		// Path defaults to ~/.cache/submon/cache.json.
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
			URL      string `toml:"url"`
			AppToken string `toml:"app_token"`
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

// Dir returns the config directory (~/.config/submon).
func Dir() (string, error) {
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		return filepath.Join(v, "submon"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "submon"), nil
}

// Load reads config.toml if present and applies env overrides. A missing file
// is not an error.
func Load() (*Config, error) {
	cfg := Default()
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	path := filepath.Join(dir, "config.toml")
	if _, err := os.Stat(path); err == nil {
		if _, err := toml.DecodeFile(path, cfg); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
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

// CachePath resolves the cache file location.
func (c *Config) CachePath() (string, error) {
	if c.Cache.Path != "" {
		return c.Cache.Path, nil
	}
	if v := os.Getenv("XDG_CACHE_HOME"); v != "" {
		return filepath.Join(v, "submon", "cache.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".cache", "submon", "cache.json"), nil
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
// secrets, but keep it locked down regardless).
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// DecryptSecrets decrypts encrypted token fields in place. Plaintext values
// pass through. Returns the first decryption error encountered (fields stay
// as-is when decryption fails).
//
// 覆盖范围:每个 provider 的顶层 Token + [[keys]] 数组里每条 Key.Token。
// 顶层 Key 先按 provider 顺序解,再统一扫一遍 Keys(数组顺序在所有
// provider 之间连号,错误时仍记录首条不中断)。
func (c *Config) DecryptSecrets() error {
	fields := []*string{&c.Zhipu.Token, &c.MiniMax.Token, &c.OpenCode.Token, &c.Kimi.Token, &c.Notify.Gotify.AppToken}
	var firstErr error
	for _, f := range fields {
		v := *f
		if !secret.IsEncrypted(v) {
			continue
		}
		pt, err := secret.Decrypt(v)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		*f = pt
	}
	for _, k := range c.allKeyTokens() {
		v := *k
		if !secret.IsEncrypted(v) {
			continue
		}
		pt, err := secret.Decrypt(v)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		*k = pt
	}
	return firstErr
}

// allKeyTokens 收集所有 [[provider.keys]] 的 token 字段指针。
// 调用方负责解密后写回(指针原位修改)。
func (c *Config) allKeyTokens() []*string {
	var out []*string
	for _, k := range c.Zhipu.Keys {
		out = append(out, &k.Token)
	}
	for _, k := range c.MiniMax.Keys {
		out = append(out, &k.Token)
	}
	for _, k := range c.OpenCode.Keys {
		out = append(out, &k.Token)
	}
	for _, k := range c.Kimi.Keys {
		out = append(out, &k.Token)
	}
	return out
}

// ProviderKeys 返回 provider 的全部已配置 Key。规则:
//
//  1. 顶层 Token 非空 → 规范化为 label = "default" 的 Key(向老格式兼容)。
//  2. [[keys]] 里的每条:空 label 自动编号 "key-N"(N 从 1 起,跨 provider 全局唯一
//     仅按当前 provider 计数);非空 label 保留用户给定值。
//  3. 同 label 出现多次:后面的覆盖前面(防止两份配置 merge 时悄无声息)。
//  4. 返回值不含空 token 的 Key,调用方拿到的都是"至少有 Token 的" 列表。
//
// 调用方拿到的是副本,修改不影响 Config。
func (c *Config) ProviderKeys(p model.Provider) []ProviderKey {
	switch p {
	case model.ProviderZhipu:
		return mergeKeys(c.Zhipu.Token, c.Zhipu.Keys)
	case model.ProviderMinimax:
		return mergeKeys(c.MiniMax.Token, c.MiniMax.Keys)
	case model.ProviderOpenCode:
		return mergeKeys(c.OpenCode.Token, c.OpenCode.Keys)
	case model.ProviderKimi:
		return mergeKeys(c.Kimi.Token, c.Kimi.Keys)
	}
	return nil
}

// mergeKeys 合并顶层 default token 与 [[keys]] 数组。
//
// 语义:
//   - 顶层 Token 总是规范化为 Label="default",并放在第一位。
//   - [[keys]] 里的空 Token 跳过(无效配置)。
//   - 空 Label 自动编号 "key-N"(N 在当前合并列表中递增;与 default
//     的 "default" 不冲突,也不冲突用户给的固定 label)。
//   - 同 Label 出现多次:后者覆盖前者(TOML 里后写的赢),原位替换,顺序保留。
func mergeKeys(defaultToken string, keys []ProviderKey) []ProviderKey {
	out := make([]ProviderKey, 0, len(keys)+1)
	if defaultToken != "" {
		out = append(out, ProviderKey{Label: "default", Token: defaultToken})
	}
	autoLabels := map[string]bool{}
	for _, k := range keys {
		if k.Token == "" {
			continue
		}
		label := k.Label
		if label == "" {
			label = nextAutoLabel(autoLabels)
		}
		k.Label = label
		replaced := false
		for i := range out {
			if out[i].Label == label {
				out[i] = k
				replaced = true
				break
			}
		}
		if !replaced {
			out = append(out, k)
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
