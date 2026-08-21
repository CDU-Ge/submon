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

// Result reports what happened for one provider (one Key) during a refresh pass.
type Result struct {
	Provider model.Provider
	KeyLabel string // 多 key 模式:此 snapshot 对应的 Key;单 key 老路径为空
	Snapshot *model.Snapshot
	Err      error
}

// One 强制刷新默认 Key(单 key 老路径)。多 key 场景请用 Many。
func One(ctx context.Context, name model.Provider, cfg *config.Config, log *slog.Logger) Result {
	res := Result{Provider: name}
	src, err := provider.Build(name, cfg)
	if err != nil {
		res.Err = err
		recordKey(ctx, cfg, name, "", res.Err, log)
		return res
	}
	snap, err := src.Fetch(ctx)
	if err != nil {
		res.Err = err
		recordKey(ctx, cfg, name, "", res.Err, log)
		return res
	}
	res.Snapshot = snap
	storeKey(ctx, cfg, name, "", func(e *cache.Entry) {
		e.Snapshot = snap
		e.FetchedAt = time.Now()
		e.LastErr = ""
	}, log)
	return res
}

// Many 刷新 provider 下全部已配置 Key,每个 Key 独立取一次。
// 顺序与 cfg.ProviderKeys 一致(顶层 default 在前,[[keys]] 在后)。
// 返回的 []Result 长度等于成功构造的 Source 数(失败的 Key 在 Err 里)。
//
// 取数语义对每个 Key 都是独立的 HTTP 调用 — provider 通常按账号独立
// 计数配额,fetch 一次覆盖多账号既不准确也无法合并窗口。
func Many(ctx context.Context, name model.Provider, cfg *config.Config, log *slog.Logger) []Result {
	srcs, err := provider.BuildAll(name, cfg)
	if err != nil {
		return []Result{{Provider: name, Err: err}}
	}
	out := make([]Result, 0, len(srcs))
	for _, ls := range srcs {
		r := Result{Provider: name, KeyLabel: ls.Label}
		snap, err := ls.Fetch(ctx)
		if err != nil {
			r.Err = err
			recordKey(ctx, cfg, name, ls.Label, err, log)
			out = append(out, r)
			continue
		}
		r.Snapshot = snap
		storeKey(ctx, cfg, name, ls.Label, func(e *cache.Entry) {
			e.Snapshot = snap
			e.FetchedAt = time.Now()
			e.LastErr = ""
		}, log)
		out = append(out, r)
	}
	return out
}

// recordErr persists the failure while keeping the last good snapshot.
// 单 key 老路径,label = ""。
func recordErr(ctx context.Context, cfg *config.Config, name model.Provider, fetchErr error, log *slog.Logger) {
	recordKey(ctx, cfg, name, "", fetchErr, log)
}

// recordKey 同 recordErr 但支持 label(多 key 场景)。
func recordKey(_ context.Context, cfg *config.Config, name model.Provider, label string, fetchErr error, log *slog.Logger) {
	storeKey(nil, cfg, name, label, func(e *cache.Entry) {
		e.LastErr = fetchErr.Error()
	}, log)
	if log != nil {
		log.Warn("refresh failed", "provider", name, "label", label, "err", fetchErr.Error())
	}
}

// storeSnapshot loads the cache, mutates the provider's entry and saves.
// 单 key 老路径(label = "")。
func storeSnapshot(ctx context.Context, cfg *config.Config, name model.Provider, mutate func(*cache.Entry), log *slog.Logger) {
	storeKey(ctx, cfg, name, "", mutate, log)
}

// storeKey 通用 storeSnapshot:label 非空时走 SetLabeled。
func storeKey(_ context.Context, cfg *config.Config, name model.Provider, label string, mutate func(*cache.Entry), log *slog.Logger) {
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
	e := f.GetLabeled(name, label)
	if e == nil {
		e = &cache.Entry{}
		f.SetLabeled(name, label, e)
	}
	mutate(e)
	if err := store.Save(f); err != nil && log != nil {
		log.Error("cache save", "err", err)
	}
}
