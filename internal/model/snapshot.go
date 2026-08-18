// Package model defines the provider-agnostic subscription snapshot that all
// commands (status, statusline, watch) consume.
package model

import "time"

// Provider identifies a subscription source.
type Provider string

const (
	ProviderZhipu    Provider = "zhipu"
	ProviderMinimax  Provider = "minimax"
	ProviderOpenCode Provider = "opencode"
)

// AllProviders 列出全部已知 provider(展示顺序)。
func AllProviders() []Provider {
	return []Provider{ProviderZhipu, ProviderMinimax, ProviderOpenCode}
}

// ValidProvider 报告 p 是否为已知 provider。
func ValidProvider(p Provider) bool {
	for _, v := range AllProviders() {
		if v == p {
			return true
		}
	}
	return false
}

// Window is a usage window normalized to "remaining" semantics across providers.
// Both Zhipu and MiniMax expose a ~5h short window and a weekly long window.
type Window struct {
	// RemainingPct is the remaining percentage (0-100). Nil if the API did
	// not report one (e.g. MiniMax weekly unlimited plans).
	RemainingPct *float64 `json:"remaining_pct,omitempty"`
	// UsedPct is the used percentage (100 - remaining), kept for display symmetry.
	UsedPct *float64 `json:"used_pct,omitempty"`
	// RemainingCount / TotalCount are usage counts (requests) when reported.
	// Nil when the provider only reports percentages (Zhipu token windows).
	RemainingCount *int64 `json:"remaining_count,omitempty"`
	TotalCount     *int64 `json:"total_count,omitempty"`
	// ResetAt is when the window rolls over. Nil when unknown.
	ResetAt *time.Time `json:"reset_at,omitempty"`
	// StartedAt is when the window began. Nil when unknown; filled by
	// providers that know the window length (MiniMax) or reset time only
	// (Zhipu: derived as reset − 5h/7d once, at fetch time).
	StartedAt *time.Time `json:"started_at,omitempty"`
	// Label is a human description, e.g. "5h", "week", "month".
	Label string `json:"label"`
}

// PredictedWastePct 按当前使用速率外推:重置时预计剩余(将被浪费)的百分比。
//
//	rate = UsedPct / elapsed;projected = rate × until
//	waste = RemainingPct − projected(截断到 [0,100])
//
// 返回 nil 表示无法预测:缺字段、窗口刚开始(<5min,速率噪声大)、
// 尚无使用(UsedPct=0,无从外推——这是"零使用"极端值,由调用方单独处理)。
func (w *Window) PredictedWastePct(now time.Time) *float64 {
	if w == nil || w.RemainingPct == nil || w.UsedPct == nil ||
		w.ResetAt == nil || w.StartedAt == nil {
		return nil
	}
	elapsed := now.Sub(*w.StartedAt)
	until := w.ResetAt.Sub(now)
	if elapsed <= 5*time.Minute || until <= 0 || *w.UsedPct <= 0 {
		return nil
	}
	projectedUse := *w.UsedPct * float64(until) / float64(elapsed)
	waste := *w.RemainingPct - projectedUse
	if waste < 0 {
		waste = 0
	}
	if waste > 100 {
		waste = 100
	}
	return &waste
}

// Snapshot is the normalized state of one provider at a point in time.
type Snapshot struct {
	Provider Provider `json:"provider"`
	// PlanLevel is the subscription tier when known (e.g. Zhipu "pro", MiniMax plan name).
	PlanLevel string `json:"plan_level,omitempty"`
	// ShortWindow is the ~5-hour rolling token window.
	ShortWindow *Window `json:"short_window,omitempty"`
	// LongWindow is the weekly window.
	LongWindow *Window `json:"long_window,omitempty"`
	// MonthlyToolWindow is Zhipu's monthly MCP/tool call quota (TIME_LIMIT).
	MonthlyToolWindow *Window `json:"monthly_tool_window,omitempty"`
	// SubscriptionExpiry is the subscription end date (MiniMax) when known.
	SubscriptionExpiry *time.Time `json:"subscription_expiry,omitempty"`
	// Models lists per-model token usage over the query span (Zhipu).
	Models []ModelUsage `json:"models,omitempty"`
	// RawWarning carries non-fatal provider notes (missing fields etc).
	RawWarning string    `json:"raw_warning,omitempty"`
	FetchedAt  time.Time `json:"fetched_at"`
}

// ModelUsage is per-model aggregate usage (Zhipu model-usage endpoint).
type ModelUsage struct {
	Name        string `json:"name"`
	TotalTokens int64  `json:"total_tokens"`
}

// CriticalPct returns the lowest remaining percentage across all windows,
// used by watch's threshold state machine. Returns nil when no window
// reports a percentage.
func (s *Snapshot) CriticalPct() *float64 {
	var lowest *float64
	for _, w := range []*Window{s.ShortWindow, s.LongWindow, s.MonthlyToolWindow} {
		if w == nil || w.RemainingPct == nil {
			continue
		}
		if lowest == nil || *w.RemainingPct < *lowest {
			v := *w.RemainingPct
			lowest = &v
		}
	}
	return lowest
}
