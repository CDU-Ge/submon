package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
)

// Zhipu queries open.bigmodel.cn / api.z.ai coding-plan monitors.
//
// Endpoints (auth: raw ANTHROPIC_AUTH_TOKEN in the Authorization header):
//   - GET {base}/api/monitor/usage/quota/limit   → windowed quota limits
//   - GET {base}/api/monitor/usage/model-usage   → hourly per-model token usage
//
// The original mjs script mapped every TOKENS_LIMIT to "5 Hour", which drops
// the weekly window. The limits array distinguishes windows by (type, unit,
// number); observed pairs:
//
//	TOKENS_LIMIT unit=3 number=5  → rolling 5-hour token window
//	TOKENS_LIMIT unit=6 number=1  → weekly token window
//	TIME_LIMIT   unit=5 number=1  → monthly MCP/tool call quota
//
// Zhipu's `percentage` is *used* percent (verified against live values), the
// inverse of MiniMax's remaining_percent; we normalize to remaining here.
type Zhipu struct {
	// BaseURL is the monitor origin, e.g. "https://open.bigmodel.cn".
	BaseURL string
	// Token is the raw ANTHROPIC_AUTH_TOKEN value.
	Token string
	// Client overrides the HTTP client (tests). Nil uses a 10s-timeout default.
	Client *http.Client
}

func (z *Zhipu) Name() model.Provider { return model.ProviderZhipu }

func (z *Zhipu) client() *http.Client {
	if z.Client != nil {
		return z.Client
	}
	return &http.Client{Timeout: HTTPTimeout}
}

// quotaLimitResponse mirrors the quota/limit endpoint payload.
type quotaLimitResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		Limits []quotaLimit `json:"limits"`
		Level  string       `json:"level"`
	} `json:"data"`
}

type quotaLimit struct {
	Type          string        `json:"type"`
	Unit          int           `json:"unit"`
	Number        int           `json:"number"`
	Percentage    *float64      `json:"percentage"`   // used %
	Usage         *int64        `json:"usage"`        // TIME_LIMIT total quota
	CurrentValue  *int64        `json:"currentValue"` // TIME_LIMIT used count
	Remaining     *int64        `json:"remaining"`
	NextResetTime *int64        `json:"nextResetTime"` // epoch ms
	UsageDetails  []usageDetail `json:"usageDetails"`
}

type usageDetail struct {
	ModelCode string `json:"modelCode"`
	Usage     int64  `json:"usage"`
}

type modelUsageResponse struct {
	Code int    `json:"code"`
	Msg  string `json:"msg"`
	Data struct {
		TotalUsage struct {
			TotalModelCallCount int64        `json:"totalModelCallCount"`
			TotalTokensUsage    int64        `json:"totalTokensUsage"`
			ModelSummaryList    []modelEntry `json:"modelSummaryList"`
		} `json:"totalUsage"`
	} `json:"data"`
}

type modelEntry struct {
	ModelName   string `json:"modelName"`
	TotalTokens int64  `json:"totalTokens"`
	SortOrder   int    `json:"sortOrder"`
}

func (z *Zhipu) Fetch(ctx context.Context) (*model.Snapshot, error) {
	if z.Token == "" {
		return nil, &ErrNotConfigured{Reason: "缺少 zhipu token:设置 ANTHROPIC_AUTH_TOKEN 或运行 `submon login` 配置 zhipu 账号"}
	}
	if z.BaseURL == "" {
		return nil, &ErrNotConfigured{Reason: "缺少 zhipu base_url:设置 ANTHROPIC_BASE_URL 或运行 `submon login` 配置 zhipu 账号"}
	}

	snap := &model.Snapshot{Provider: model.ProviderZhipu, FetchedAt: time.Now()}

	qr, err := z.fetchQuotaLimit(ctx)
	if err != nil {
		return nil, err
	}
	snap.PlanLevel = qr.Data.Level
	var warnings []string
	seen := map[string]bool{}
	for _, l := range qr.Data.Limits {
		w, kind, err := z.parseLimit(l)
		if err != nil {
			warnings = append(warnings, err.Error())
			continue
		}
		if kind == "" || seen[kind] {
			continue
		}
		seen[kind] = true
		switch kind {
		case "short":
			snap.ShortWindow = w
		case "long":
			snap.LongWindow = w
		case "monthly_tool":
			snap.MonthlyToolWindow = w
		}
	}
	if snap.ShortWindow == nil {
		warnings = append(warnings, "no 5h window (TOKENS_LIMIT unit=3) in response")
	}
	if snap.LongWindow == nil {
		warnings = append(warnings, "no weekly window (TOKENS_LIMIT unit=6) in response")
	}
	snap.RawWarning = strings.Join(warnings, "; ")

	// Model usage is supplementary; failure degrades to a warning.
	mr, err := z.fetchModelUsage(ctx)
	if err == nil {
		for _, m := range mr.Data.TotalUsage.ModelSummaryList {
			snap.Models = append(snap.Models, model.ModelUsage{Name: m.ModelName, TotalTokens: m.TotalTokens})
		}
	}
	return snap, nil
}

// parseLimit converts one quota/limit entry into a Window plus the slot it
// belongs to ("short", "long", "monthly_tool"); kind is empty for entries we
// intentionally skip (other TIME_LIMIT rows).
func (z *Zhipu) parseLimit(l quotaLimit) (*model.Window, string, error) {
	isTokens := l.Type == "TOKENS_LIMIT"
	isTime := l.Type == "TIME_LIMIT"

	var kind string
	switch {
	case isTokens && l.Unit == 3 && l.Number == 5:
		kind = "short"
	case isTokens && l.Unit == 6 && l.Number == 1:
		kind = "long"
	case isTime && l.Unit == 5 && l.Number == 1:
		kind = "monthly_tool"
	case isTokens || isTime:
		// Known type but unknown unit/number combo: surface it rather than
		// silently mislabeling the window.
		return nil, "", fmt.Errorf("unrecognized limit entry type=%s unit=%d number=%d", l.Type, l.Unit, l.Number)
	default:
		return nil, "", nil
	}

	w := &model.Window{}
	switch kind {
	case "short":
		w.Label = "5h"
	case "long":
		w.Label = "week"
	case "monthly_tool":
		w.Label = "month"
	}

	if l.Percentage != nil {
		used := clampPct(*l.Percentage)
		rem := 100 - used
		w.UsedPct = &used
		w.RemainingPct = &rem
	}
	if l.NextResetTime != nil {
		t := time.UnixMilli(*l.NextResetTime)
		w.ResetAt = &t
		// 反推窗口起点:5h 窗口长 5h,weekly 长 7d,monthly 长 30d。
		var length time.Duration
		switch kind {
		case "short":
			length = 5 * time.Hour
		case "long":
			length = 7 * 24 * time.Hour
		case "monthly_tool":
			length = 30 * 24 * time.Hour
		}
		if length > 0 {
			st := t.Add(-length)
			w.StartedAt = &st
		}
	}
	if kind == "monthly_tool" {
		// TIME_LIMIT reports absolute counts.
		if l.Usage != nil {
			total := *l.Usage
			w.TotalCount = &total
		}
		if l.CurrentValue != nil {
			used := *l.CurrentValue
			rem := int64(0)
			if w.TotalCount != nil {
				rem = max(0, *w.TotalCount-used)
			} else if l.Remaining != nil {
				rem = *l.Remaining
			}
			w.RemainingCount = &rem
		}
	}
	return w, kind, nil
}

func (z *Zhipu) fetchQuotaLimit(ctx context.Context) (*quotaLimitResponse, error) {
	var out quotaLimitResponse
	if err := z.getJSON(ctx, "/api/monitor/usage/quota/limit", nil, &out); err != nil {
		return nil, fmt.Errorf("quota/limit: %w", err)
	}
	if out.Code != 200 {
		return nil, fmt.Errorf("quota/limit: API error code=%d msg=%s", out.Code, out.Msg)
	}
	return &out, nil
}

func (z *Zhipu) fetchModelUsage(ctx context.Context) (*modelUsageResponse, error) {
	// Time span: yesterday-at-current-hour → now, mirroring the original
	// script (the API needs an explicit hourly window).
	now := time.Now()
	start := time.Date(now.Year(), now.Month(), now.Day(), now.Hour(), 0, 0, 0, now.Location()).AddDate(0, 0, -1)
	end := now
	q := map[string]string{
		"startTime": formatZhipuTime(start),
		"endTime":   formatZhipuTime(end),
	}
	var out modelUsageResponse
	if err := z.getJSON(ctx, "/api/monitor/usage/model-usage", q, &out); err != nil {
		return nil, fmt.Errorf("model-usage: %w", err)
	}
	if out.Code != 200 {
		return nil, fmt.Errorf("model-usage: API error code=%d msg=%s", out.Code, out.Msg)
	}
	return &out, nil
}

func (z *Zhipu) getJSON(ctx context.Context, path string, query map[string]string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSuffix(z.BaseURL, "/")+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", z.Token)
	req.Header.Set("Accept-Language", "en-US,en")
	req.Header.Set("Content-Type", "application/json")
	if len(query) > 0 {
		q := req.URL.Query()
		for k, v := range query {
			q.Set(k, v)
		}
		req.URL.RawQuery = q.Encode()
	}
	resp, err := z.client().Do(req)
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

func formatZhipuTime(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

func clampPct(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 100 {
		return 100
	}
	return v
}
