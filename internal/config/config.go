// Package config loads submon configuration from ~/.config/submon/config.toml
// with environment-variable overrides.
package config

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
)

type Config struct {
	Zhipu struct {
		BaseURL string `toml:"base_url"`
		Token   string `toml:"token"`
	} `toml:"zhipu"`
	MiniMax struct {
		Token      string `toml:"token"`
		ConfigPath string `toml:"config_path"`
	} `toml:"minimax"`
	Cache struct {
		// TTLSeconds is how long a fetched snapshot is served from cache.
		// Default 300 (5 min).
		TTLSeconds int `toml:"ttl_seconds"`
		// Path defaults to ~/.cache/submon/cache.json.
		Path string `toml:"path"`
	} `toml:"cache"`
	Notify struct {
		// WarnBelowPct / CritBelowPct are *remaining* percentage thresholds.
		WarnBelowPct float64 `toml:"warn_below_pct"`
		CritBelowPct float64 `toml:"crit_below_pct"`
		Gotify struct {
			URL       string `toml:"url"`
			AppToken  string `toml:"app_token"`
			// Priority is the Gotify priority (1-10), default 5.
			Priority int `toml:"priority"`
		} `toml:"gotify"`
	} `toml:"notify"`
	Statusline struct {
		// DefaultProvider picks the provider when it can't be auto-detected
		// ("zhipu" | "minimax").
		DefaultProvider string `toml:"default_provider"`
	} `toml:"statusline"`
}

// Default returns built-in defaults.
func Default() *Config {
	c := &Config{}
	c.Cache.TTLSeconds = 300
	c.Notify.WarnBelowPct = 20
	c.Notify.CritBelowPct = 5
	c.Notify.Gotify.Priority = 5
	return c
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

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
