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
	"github.com/ge/subscription-monitor/internal/secret"
)

type Config struct {
	Zhipu struct {
		BaseURL string `toml:"base_url"`
		Token   string `toml:"token"`
	} `toml:"zhipu"`
	MiniMax struct {
		Token string `toml:"token"`
	} `toml:"minimax"`
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
		// RemindBefore: 窗口重置前多久开始"催用"提醒(如 "1h"、"30m"),0 = 关闭。
		RemindBefore string `toml:"remind_before"`
		// UseAbovePct: 窗口将重置时,剩余 ≥ 此百分比才提醒"抓紧用"
		// (剩余很少 = 已用够本,不打扰)。
		UseAbovePct float64 `toml:"use_above_pct"`
		Gotify      struct {
			URL      string `toml:"url"`
			AppToken string `toml:"app_token"`
			// Priority 是 Gotify 优先级(1-10),默认 5。
			Priority int `toml:"priority"`
		} `toml:"gotify"`
	} `toml:"notify"`
	Statusline struct {
		// DefaultProvider: 无法自动识别时的默认 provider("zhipu"|"minimax")。
		DefaultProvider string `toml:"default_provider"`
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
	c.Notify.UseAbovePct = 30
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
	if cfg.Notify.UseAbovePct <= 0 {
		cfg.Notify.UseAbovePct = 30
	}
	if cfg.Statusline.Layout == "" {
		cfg.Statusline.Layout = "a"
	}
	// 环境变量覆盖,便于临时切换方案预览:SUBMON_LAYOUT=a|b|c
	if v := os.Getenv("SUBMON_LAYOUT"); v != "" {
		cfg.Statusline.Layout = v
	}
	if cfg.Statusline.DefaultProvider == "" {
		cfg.Statusline.DefaultProvider = os.Getenv("SUBMON_PROVIDER")
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
func (c *Config) DecryptSecrets() error {
	fields := []*string{&c.Zhipu.Token, &c.MiniMax.Token, &c.Notify.Gotify.AppToken}
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
	return firstErr
}
