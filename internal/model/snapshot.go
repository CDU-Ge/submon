// Package model defines the provider-agnostic subscription snapshot that all
// commands (status, statusline, watch) consume.
package model

import "time"

// Provider identifies a subscription source.
type Provider string

const (
	ProviderZhipu   Provider = "zhipu"
	ProviderMinimax Provider = "minimax"
)

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
	// Label is a human description, e.g. "5h", "week", "month".
	Label string `json:"label"`
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
	RawWarning string `json:"raw_warning,omitempty"`
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
