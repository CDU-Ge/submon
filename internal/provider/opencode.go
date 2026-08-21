package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
)

// OpenCode queries the opencode.ai zen usage API.
//
// GET https://opencode.ai/zen/go/v1/usage (Bearer OPENCODE_API_KEY).
// Payload shape (per usage.<window>):
//
//	rolling:  status / percent / resetsAt   — rolling short window (~5h)
//	weekly:   status / percent / resetsAt   — weekly window
//	monthly:  status / percent / resetsAt   — monthly window
//
// `percent` is *used* percent (status "ok" with percent 0 = nothing consumed),
// resetsAt is RFC3339. Windows with an unbounded plan report status only —
// those degrade to nil percentage. rolling/weekly map to short/long; monthly
// has no count quota so it is surfaced as RawWarning-free extra info in the
// snapshot's MonthlyToolWindow slot (label "month").
type OpenCode struct {
	// Token is the Bearer credential (OPENCODE_API_KEY).
	Token string
	// BaseURL overrides the API origin (tests).
	BaseURL string
	// Client overrides the HTTP client (tests).
	Client *http.Client
}

func (o *OpenCode) Name() model.Provider { return model.ProviderOpenCode }

// opencodeRules 是 OpenCode 在内建 URL 检测中的全部声明。OpenCode 是
// 多入口产品,ai / com 两套 host 都可能挂着 coding / chat 子域。
func opencodeRules() []Rule {
	return []Rule{
		{Provider: model.ProviderOpenCode, Hosts: []string{
			"*.opencode.ai", "opencode.ai",
			"*.opencode.com", "opencode.com",
		}},
	}
}

const DefaultOpenCodeBaseURL = "https://opencode.ai"

func (o *OpenCode) client() *http.Client {
	if o.Client != nil {
		return o.Client
	}
	return &http.Client{Timeout: HTTPTimeout}
}

func (o *OpenCode) baseURL() string {
	if o.BaseURL != "" {
		return o.BaseURL
	}
	return DefaultOpenCodeBaseURL
}

func (o *OpenCode) resolveToken() (string, error) {
	if o.Token != "" {
		return o.Token, nil
	}
	if v := os.Getenv("OPENCODE_API_KEY"); v != "" {
		return v, nil
	}
	return "", &ErrNotConfigured{Reason: "缺少 opencode token:设置 OPENCODE_API_KEY 或配置 [opencode] token"}
}

// usageResponse mirrors the zen usage payload; every window is optional.
type usageResponse struct {
	Usage struct {
		Rolling opencodeWindow `json:"rolling"`
		Weekly  opencodeWindow `json:"weekly"`
		Monthly opencodeWindow `json:"monthly"`
	} `json:"usage"`
}

type opencodeWindow struct {
	Status    string   `json:"status"`
	Percent   *float64 `json:"percent"`   // used %
	ResetsAt  string   `json:"resetsAt"`  // RFC3339
	Total     *int64   `json:"total"`     // 请求次数上限(可为空)
	Remaining *int64   `json:"remaining"` // 剩余请求次数
}

func (o *OpenCode) Fetch(ctx context.Context) (*model.Snapshot, error) {
	token, err := o.resolveToken()
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, o.baseURL()+"/zen/go/v1/usage", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	resp, err := o.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("HTTP %d: %.200s", resp.StatusCode, string(body))
	}
	var out usageResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("usage: %w", err)
	}

	snap := &model.Snapshot{Provider: model.ProviderOpenCode, FetchedAt: time.Now()}
	snap.ShortWindow = opencodeToWindow(out.Usage.Rolling, "5h", 5*time.Hour)
	snap.LongWindow = opencodeToWindow(out.Usage.Weekly, "week", 7*24*time.Hour)
	if snap.LongWindow == nil {
		// weekly 是主窗口,缺失时退回 monthly 保证有东西可展示。
		snap.LongWindow = opencodeToWindow(out.Usage.Monthly, "month", 30*24*time.Hour)
	}
	if w := opencodeToWindow(out.Usage.Monthly, "month", 30*24*time.Hour); w != nil && snap.LongWindow != w {
		snap.MonthlyToolWindow = w
	}
	return snap, nil
}

// opencodeToWindow normalizes one window; nil when it carries no usable data.
// Window length is only used to derive StartedAt (rate extrapolation needs it).
func opencodeToWindow(w opencodeWindow, label string, length time.Duration) *model.Window {
	if w.Percent == nil && w.ResetsAt == "" && w.Total == nil {
		return nil
	}
	out := &model.Window{Label: label}
	if w.Percent != nil {
		used := clampPct(*w.Percent)
		rem := 100 - used
		out.UsedPct = &used
		out.RemainingPct = &rem
	}
	if w.ResetsAt != "" {
		if t, err := time.Parse(time.RFC3339, w.ResetsAt); err == nil {
			reset := t
			out.ResetAt = &reset
			st := t.Add(-length)
			out.StartedAt = &st
		}
	}
	if w.Total != nil && *w.Total > 0 {
		total := *w.Total
		out.TotalCount = &total
		if w.Remaining != nil {
			rem := *w.Remaining
			out.RemainingCount = &rem
		}
	}
	return out
}
