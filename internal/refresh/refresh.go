// Package refresh coordinates fetching + cache updates. It is the only code
// path (besides `status --force`) that writes snapshots, so statusline can
// stay a pure cache reader.
package refresh

import (
	"context"
	"log/slog"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/provider"
)

// Result reports what happened for one provider during a refresh pass.
type Result struct {
	Provider model.Provider
	Snapshot *model.Snapshot
	Err      error
}

// One force-fetches a provider and updates the cache entry (snapshot on
// success, last_error on failure — statusline serves stale data either way).
func One(ctx context.Context, name model.Provider, cfg *config.Config, log *slog.Logger) Result {
	res := Result{Provider: name}
	src, err := provider.Build(name, cfg)
	if err != nil {
		res.Err = err
		recordErr(ctx, cfg, name, res.Err, log)
		return res
	}
	snap, err := src.Fetch(ctx)
	if err != nil {
		res.Err = err
		recordErr(ctx, cfg, name, res.Err, log)
		return res
	}
	res.Snapshot = snap
	storeSnapshot(ctx, cfg, name, func(e *cache.Entry) {
		e.Snapshot = snap
		e.FetchedAt = time.Now()
		e.LastErr = ""
	}, log)
	return res
}

// recordErr persists the failure while keeping the last good snapshot.
func recordErr(ctx context.Context, cfg *config.Config, name model.Provider, fetchErr error, log *slog.Logger) {
	storeSnapshot(ctx, cfg, name, func(e *cache.Entry) {
		e.LastErr = fetchErr.Error()
	}, log)
	if log != nil {
		log.Warn("refresh failed", "provider", name, "err", fetchErr.Error())
	}
}

// storeSnapshot loads the cache, mutates the provider's entry and saves.
func storeSnapshot(_ context.Context, cfg *config.Config, name model.Provider, mutate func(*cache.Entry), log *slog.Logger) {
	path, err := cfg.CachePath()
	if err != nil {
		if log != nil {
			log.Error("cache path", "err", err)
		}
		return
	}
	store := cache.New(path)
	f, err := store.Load()
	if err != nil {
		if log != nil {
			log.Error("cache load", "err", err)
		}
		return
	}
	e := f.Get(name)
	if e == nil {
		e = &cache.Entry{}
		f.Set(name, e)
	}
	mutate(e)
	if err := store.Save(f); err != nil && log != nil {
		log.Error("cache save", "err", err)
	}
}
