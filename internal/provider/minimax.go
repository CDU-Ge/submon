package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
)

// MiniMax queries the MiniMax coding-plan remains API.
//
// GET https://www.minimaxi.com/v1/api/openplatform/coding_plan/remains
// (Bearer token). Payload shape (per model_remains[i]):
//
//	current_interval_total_count / current_interval_remaining_percent / remains_time
//	current_weekly_total_count    / current_weekly_remaining_percent    / weekly_remains_time
//	start_time / end_time / model_name
//
// Credentials come from config ([minimax] token) or MINIMAX_TOKEN env; the
// legacy ~/.minimax-config.json file is only read once by
// `submon generate config` to migrate the token.
//
// MiniMax's remaining_percent is literally "remaining %" (unlike Zhipu's used
// percentage), so it maps directly. remains_time / weekly_remains_time are
// durations in milliseconds until the window resets.
//
// Subscription expiry comes from the combo/package endpoint; failure there is
// non-fatal (the upstream CLI also treats it as optional).
type MiniMax struct {
	// Token is the Bearer credential. When empty, MINIMAX_TOKEN env is tried.
	Token string
	// BaseURL overrides the API origin (tests).
	BaseURL string
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

func (m *MiniMax) Name() model.Provider { return model.ProviderMinimax }

func (m *MiniMax) client() *http.Client {
	if m.Client != nil {
		return m.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (m *MiniMax) baseURL() string {
	if m.BaseURL != "" {
		return m.BaseURL
	}
	return "https://www.minimaxi.com"
}

type minimaxConfig struct {
	Token   string `json:"token"`
	GroupID string `json:"groupId"`
}

// ReadLegacyMinimaxToken extracts the token from ~/.minimax-config.json.
// Used by `submon generate config` to migrate credentials; empty when the
// file is missing or has no token.
func ReadLegacyMinimaxToken() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	path := filepath.Join(home, ".minimax-config.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	var cfg minimaxConfig
	if json.Unmarshal(data, &cfg) != nil {
		return ""
	}
	return cfg.Token
}

func (m *MiniMax) resolveToken() (string, error) {
	if m.Token != "" {
		return m.Token, nil
	}
	if v := os.Getenv("MINIMAX_TOKEN"); v != "" {
		return v, nil
	}
	return "", &ErrNotConfigured{Reason: "缺少 minimax token:设置 MINIMAX_TOKEN 或配置 [minimax] token"}
}

// remainsResponse: fields live at the top level (no data wrapper);
// status comes from base_resp, matching the upstream CLI's parsing.
type remainsResponse struct {
	ModelRemains []modelRemains `json:"model_remains"`
	BaseResp     struct {
		StatusCode int    `json:"status_code"`
		StatusMsg  string `json:"status_msg"`
	} `json:"base_resp"`
}

type modelRemains struct {
	ModelName                       string   `json:"model_name"`
	StartTime                       int64    `json:"start_time"` // epoch ms
	EndTime                         int64    `json:"end_time"`   // epoch ms
	CurrentIntervalTotalCount       int64    `json:"current_interval_total_count"`
	CurrentIntervalRemainingPercent *float64 `json:"current_interval_remaining_percent"`
	RemainsTime                     int64    `json:"remains_time"` // ms until 5h reset
	CurrentWeeklyTotalCount         int64    `json:"current_weekly_total_count"`
	CurrentWeeklyRemainingPercent   *float64 `json:"current_weekly_remaining_percent"`
	WeeklyRemainsTime               int64    `json:"weekly_remains_time"` // ms until weekly reset
}

type subscribeResponse struct {
	CurrentSubscribe struct {
		CurrentSubscribeEndTime string `json:"current_subscribe_end_time"`
	} `json:"current_subscribe"`
}

func (m *MiniMax) Fetch(ctx context.Context) (*model.Snapshot, error) {
	token, err := m.resolveToken()
	if err != nil {
		return nil, err
	}

	remains, err := m.fetchRemains(ctx, token)
	if err != nil {
		return nil, err
	}
	if len(remains.ModelRemains) == 0 {
		return nil, fmt.Errorf("minimax: empty model_remains")
	}

	snap := &model.Snapshot{Provider: model.ProviderMinimax, FetchedAt: time.Now()}
	// Aggregate the first model entry as the plan-level view (matches the
	// upstream CLI, which renders model_remains[0] as the headline).
	first := remains.ModelRemains[0]
	snap.PlanLevel = first.ModelName

	now := time.Now()
	if pct := first.CurrentIntervalRemainingPercent; pct != nil {
		rem := clampPct(*pct)
		used := 100 - rem
		w := &model.Window{
			Label:        "5h",
			RemainingPct: &rem,
			UsedPct:      &used,
		}
		if first.CurrentIntervalTotalCount > 0 {
			total := first.CurrentIntervalTotalCount
			remaining := int64(float64(total) * rem / 100)
			w.TotalCount = &total
			w.RemainingCount = &remaining
		}
		if first.RemainsTime > 0 {
			t := now.Add(time.Duration(first.RemainsTime) * time.Millisecond)
			w.ResetAt = &t
		}
		snap.ShortWindow = w
	}

	weeklyUnlimited := first.CurrentWeeklyTotalCount == 0 && first.CurrentWeeklyRemainingPercent == nil
	if !weeklyUnlimited {
		w := &model.Window{Label: "week"}
		if pct := first.CurrentWeeklyRemainingPercent; pct != nil {
			rem := clampPct(*pct)
			used := 100 - rem
			w.RemainingPct = &rem
			w.UsedPct = &used
			if first.CurrentWeeklyTotalCount > 0 {
				total := first.CurrentWeeklyTotalCount
				remaining := int64(float64(total) * rem / 100)
				w.TotalCount = &total
				w.RemainingCount = &remaining
			}
		}
		if first.WeeklyRemainsTime > 0 {
			t := now.Add(time.Duration(first.WeeklyRemainsTime) * time.Millisecond)
			w.ResetAt = &t
		}
		snap.LongWindow = w
	}

	if sub, err := m.fetchSubscription(ctx, token); err == nil && sub.CurrentSubscribe.CurrentSubscribeEndTime != "" {
		if t, err := parseMinimaxTime(sub.CurrentSubscribe.CurrentSubscribeEndTime); err == nil {
			snap.SubscriptionExpiry = &t
		}
	}
	return snap, nil
}

func (m *MiniMax) fetchRemains(ctx context.Context, token string) (*remainsResponse, error) {
	var out remainsResponse
	err := m.getJSON(ctx, token, "/v1/api/openplatform/coding_plan/remains", &out)
	if err != nil {
		return nil, fmt.Errorf("remains: %w", err)
	}
	if out.BaseResp.StatusCode != 0 {
		return nil, fmt.Errorf("remains: API error code=%d msg=%s", out.BaseResp.StatusCode, out.BaseResp.StatusMsg)
	}
	return &out, nil
}

func (m *MiniMax) fetchSubscription(ctx context.Context, token string) (*subscribeResponse, error) {
	var out subscribeResponse
	// Optional endpoint; any failure is swallowed by the caller.
	err := m.getJSON(ctx, token, "/v1/api/openplatform/charge/combo/cycle_audio_resource_package?biz_line=2&cycle_type=1&resource_package_type=7", &out)
	if err != nil {
		return nil, err
	}
	return &out, nil
}

func (m *MiniMax) getJSON(ctx context.Context, token, path string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, m.baseURL()+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Referer", "https://platform.minimaxi.com/")
	resp, err := m.client().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, string(body))
	}
	return json.Unmarshal(body, out)
}

// parseMinimaxTime accepts RFC3339 and the "2006-01-02 15:04:05" shapes seen
// in MiniMax payloads.
func parseMinimaxTime(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	return time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
}
