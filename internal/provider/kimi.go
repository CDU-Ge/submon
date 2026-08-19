package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/model"
)

// flexFloat 接受 JSON number 与 string-编码 number。Kimi 真实 API 在不同端点
// 上同一字段(limit / used / remaining / reset_in / duration)交替出现两种
// 类型,缺哪个都会让 `*float64` 拒绝整个 payload。这是上游已经观察到的事实
// 形态,所以写在 kimi.go 里 — 任何后续遇到相同形态的 provider 直接 import。
type flexFloat float64

func (f *flexFloat) UnmarshalJSON(data []byte) error {
	// 先试 number(JSON 字面数字、RFC3339 之外的科学计数都走 json.Number)。
	var n json.Number
	if err := json.Unmarshal(data, &n); err == nil {
		if v, err := n.Float64(); err == nil {
			*f = flexFloat(v)
			return nil
		}
	}
	// 再试 string — 真实 API 偶尔把数字包成 "1000"。
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
		if err != nil {
			return fmt.Errorf("flexFloat: cannot parse %q: %w", s, err)
		}
		*f = flexFloat(v)
		return nil
	}
	return fmt.Errorf("flexFloat: cannot unmarshal %s as number", string(data))
}

func (f flexFloat) Float64() float64 { return float64(f) }

// Kimi queries kimi.com Coding Plan usage.
//
// Endpoints (auth: Bearer KIMI_CODING_API_KEY, User-Agent: KimiCLI/1.6):
//
//	GET {base}/usages   (primary)
//	GET {base}/usage    (404 fallback, mirroring the upstream reference)
//
// Default base URL: https://api.kimi.com/coding/v1
//
// Two payload shapes (informal field semantics from upstream tool):
//
//	Shape A (preferred):
//	  {
//	    "data": [
//	      {"model_name": "all", "used": N, "limit": M, "remaining": R, "resetTime": "..."},
//	      {"model_name": "kimi-k2", "window": {"duration": 5, "timeUnit": "HOUR"}, "used": ..., "limit": ...},
//	      ...
//	    ]
//	  }
//
//	Shape B (fallback):
//	  {
//	    "usage": {"used": ..., "limit": ..., "remaining": ..., "resetTime": "..."},
//	    "limits": [
//	      {"window": {"duration": 5, "timeUnit": "HOUR"},
//	       "detail": {"used": ..., "limit": ..., "remaining": ...}},
//	      ...
//	    ]
//	  }
//
// Window classification (duration + timeUnit):
//
//	5 HOUR  → ShortWindow (5h); if multiple model rows, take LOWEST remaining%.
//	7 DAY   → LongWindow (week); also shape A's "all" row and shape B's "usage".
//	30 DAY / 1 MONTH → MonthlyToolWindow (WebBridge optional monthly credits).
//
// The "percent" presented upstream is *used* percent; we normalize to remaining
// (consistent with zhipu/opencode conventions).
type Kimi struct {
	// Token is the Bearer credential (KIMI_CODING_API_KEY).
	Token string
	// BaseURL overrides the API origin (tests; default DefaultKimiBaseURL).
	BaseURL string
	// Client overrides the HTTP client (tests). Nil uses a 10s-timeout default.
	Client *http.Client
}

func (k *Kimi) Name() model.Provider { return model.ProviderKimi }

// kimiRules 是 Kimi 在内建 URL 检测中的全部声明。两套 host:
//   - api.kimi.com — Kimi Coding Plan(/coding/v1 path 是强信号)
//   - moonshot.cn / moonshot.com — Moonshot 系,历史命名,保留
func kimiRules() []Rule {
	return []Rule{
		// Coding Plan 用 path 前缀窄化:任何 host 上挂着 /coding/v1 都视为 Kimi,
		// 这是用户私有代理场景里"推断 api 端点"的关键信号。
		{Provider: model.ProviderKimi, PathPrefix: "/coding/v1"},
		{Provider: model.ProviderKimi, Hosts: []string{
			"*.kimi.com", "kimi.com",
			"*.moonshot.cn", "moonshot.cn",
			"*.moonshot.com", "moonshot.com",
		}},
	}
}

const DefaultKimiBaseURL = "https://api.kimi.com/coding/v1"

func (k *Kimi) client() *http.Client {
	if k.Client != nil {
		return k.Client
	}
	return &http.Client{Timeout: 10 * time.Second}
}

func (k *Kimi) baseURL() string {
	if k.BaseURL != "" {
		return k.BaseURL
	}
	return DefaultKimiBaseURL
}

func (k *Kimi) resolveToken() (string, error) {
	if k.Token != "" {
		return k.Token, nil
	}
	if v := os.Getenv("KIMI_CODING_API_KEY"); v != "" {
		return v, nil
	}
	if v := os.Getenv("KIMI_API_KEY"); v != "" {
		return v, nil
	}
	return "", &ErrNotConfigured{Reason: "缺少 kimi token:设置 KIMI_CODING_API_KEY/KIMI_API_KEY 或配置 [kimi] token"}
}

// httpStatusError lets the caller distinguish 404 (trigger fallback) from
// other failures without leaking the upstream raw body shape.
type httpStatusError struct {
	Code int
	Body string
}

func (e *httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %.200s", e.Code, e.Body)
}

// kimiWindowSpec describes a window's length; duration+timeUnit lets us
// normalize to Go duration.
type kimiWindowSpec struct {
	Duration *flexFloat `json:"duration"`
	TimeUnit string     `json:"timeUnit"`
}

// kimiWindowDetail is the {used, limit, remaining} block; reset fields ride
// along on the parent row in shape A. The real API also nests resetTime inside
// the detail (shape B limits[].detail.resetTime), so we keep it here to avoid
// losing the field — ResetAt would otherwise be nil for short windows in the
// shape B path.
type kimiWindowDetail struct {
	Used      *flexFloat `json:"used"`
	Limit     *flexFloat `json:"limit"`
	Remaining *flexFloat `json:"remaining"`
	ResetTime string     `json:"resetTime,omitempty"`
	ResetAt   string     `json:"reset_at,omitempty"`
	ResetIn   *flexFloat `json:"reset_in,omitempty"`
}

// kimiUsageRow is the unified shape: in shape A it appears directly in
// `data[]`, with detail fields flat on the row; in shape B it appears in
// `limits[]`, with detail nested under `detail`.
type kimiUsageRow struct {
	ModelName string            `json:"model_name"`
	Name      string            `json:"name"`
	Window    *kimiWindowSpec   `json:"window,omitempty"`
	Detail    *kimiWindowDetail `json:"detail,omitempty"`
	// shape A inline fields; shape B omits these (uses Detail).
	Used      *flexFloat `json:"used,omitempty"`
	Limit     *flexFloat `json:"limit,omitempty"`
	Remaining *flexFloat `json:"remaining,omitempty"`
	// Reset hints (any of these can be missing):
	ResetTime string     `json:"resetTime,omitempty"`
	ResetAt   string     `json:"reset_at,omitempty"`
	ResetIn   *flexFloat `json:"reset_in,omitempty"`
}

type kimiResponseA struct {
	Data []kimiUsageRow `json:"data"`
}

type kimiResponseB struct {
	Usage  *kimiUsageRow  `json:"usage"`
	Limits []kimiUsageRow `json:"limits"`
}

func (k *Kimi) Fetch(ctx context.Context) (*model.Snapshot, error) {
	token, err := k.resolveToken()
	if err != nil {
		return nil, err
	}

	body, err := k.getWithFallback(ctx, token)
	if err != nil {
		return nil, err
	}

	snap := &model.Snapshot{Provider: model.ProviderKimi, FetchedAt: time.Now()}
	var warnings []string

	// Shape A first: row array. The "data" key always exists even when empty,
	// so we use the row count as the discriminating signal.
	var shapeA kimiResponseA
	if err := json.Unmarshal(body, &shapeA); err == nil && len(shapeA.Data) > 0 {
		parseKimiShapeA(shapeA.Data, snap, &warnings)
	} else {
		// Shape B fallback.
		var shapeB kimiResponseB
		if err := json.Unmarshal(body, &shapeB); err != nil {
			return nil, fmt.Errorf("usage: %w", err)
		}
		if shapeB.Usage == nil && len(shapeB.Limits) == 0 {
			return nil, fmt.Errorf("usage: empty payload")
		}
		parseKimiShapeB(shapeB, snap, &warnings)
	}

	if snap.ShortWindow == nil {
		warnings = append(warnings, "no 5h window (duration=5 timeUnit=HOUR) in response")
	}
	if snap.LongWindow == nil {
		warnings = append(warnings, "no weekly window (all-row or 7d) in response")
	}
	snap.RawWarning = strings.Join(warnings, "; ")
	return snap, nil
}

// getWithFallback hits /usages, retries /usage on 404 (mirrors upstream tool).
func (k *Kimi) getWithFallback(ctx context.Context, token string) ([]byte, error) {
	if body, err := k.doRequest(ctx, token, "/usages"); err == nil {
		return body, nil
	} else if !isHTTPNotFound(err) {
		return nil, err
	}
	return k.doRequest(ctx, token, "/usage")
}

func (k *Kimi) doRequest(ctx context.Context, token, path string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, k.baseURL()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("User-Agent", "KimiCLI/1.6")
	req.Header.Set("Accept", "application/json")
	resp, err := k.client().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{Code: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}

func isHTTPNotFound(err error) bool {
	var hs *httpStatusError
	if errors.As(err, &hs) {
		return hs.Code == http.StatusNotFound
	}
	return false
}

// parseKimiShapeA: "all" row (no window) is the weekly aggregate; per-model
// rows with a window are classified by duration. For 5h windows across
// multiple models, take the LOWEST remaining% (most constrained view).
func parseKimiShapeA(rows []kimiUsageRow, snap *model.Snapshot, warnings *[]string) {
	var shortCandidate *model.Window
	for _, r := range rows {
		if r.ModelName == "all" || r.Window == nil {
			if w := kimiRowToWindow(r, "week", 7*24*time.Hour); w != nil {
				snap.LongWindow = w
			}
			continue
		}
		length, ok := kimiWindowLength(*r.Window)
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("unknown window %s/%v", r.Window.TimeUnit, derefFloat(r.Window.Duration)))
			continue
		}
		switch length {
		case 5 * time.Hour:
			w := kimiRowToWindow(r, "5h", length)
			if w == nil {
				continue
			}
			if shortCandidate == nil || *w.RemainingPct < *shortCandidate.RemainingPct {
				shortCandidate = w
			}
		case 7 * 24 * time.Hour:
			if w := kimiRowToWindow(r, "week", length); w != nil {
				snap.LongWindow = w
			}
		case 30 * 24 * time.Hour:
			if w := kimiRowToWindow(r, "month", length); w != nil {
				snap.MonthlyToolWindow = w
			}
		}
	}
	snap.ShortWindow = shortCandidate
}

// parseKimiShapeB: "usage" object is the weekly aggregate; "limits" array is
// classified by window length.
func parseKimiShapeB(b kimiResponseB, snap *model.Snapshot, warnings *[]string) {
	if b.Usage != nil {
		if w := kimiRowToWindow(*b.Usage, "week", 7*24*time.Hour); w != nil {
			snap.LongWindow = w
		}
	}

	var shortCandidate *model.Window
	for _, r := range b.Limits {
		if r.Window == nil {
			continue
		}
		length, ok := kimiWindowLength(*r.Window)
		if !ok {
			*warnings = append(*warnings, fmt.Sprintf("unknown window %s/%v", r.Window.TimeUnit, derefFloat(r.Window.Duration)))
			continue
		}
		detail := r.Detail
		if detail == nil {
			detail = &kimiWindowDetail{Used: r.Used, Limit: r.Limit, Remaining: r.Remaining}
		}
		// shape B 的真 API 把 resetTime 嵌在 detail 里(而不是行顶层),先看
		// detail,再看 row,两条路径都允许。
		resetTime := firstNonEmpty(detail.ResetTime, r.ResetTime)
		resetAt := firstNonEmpty(detail.ResetAt, r.ResetAt)
		resetIn := detail.ResetIn
		if resetIn == nil {
			resetIn = r.ResetIn
		}
		switch length {
		case 5 * time.Hour:
			w := kimiDetailToWindow(*detail, "5h", length, resetTime, resetAt, resetIn)
			if w == nil {
				continue
			}
			if shortCandidate == nil || *w.RemainingPct < *shortCandidate.RemainingPct {
				shortCandidate = w
			}
		case 7 * 24 * time.Hour:
			if w := kimiDetailToWindow(*detail, "week", length, resetTime, resetAt, resetIn); w != nil {
				snap.LongWindow = w
			}
		case 30 * 24 * time.Hour:
			if w := kimiDetailToWindow(*detail, "month", length, resetTime, resetAt, resetIn); w != nil {
				snap.MonthlyToolWindow = w
			}
		}
	}
	snap.ShortWindow = shortCandidate
}

// kimiWindowLength normalizes (duration, timeUnit) to Go duration. MONTH is
// treated as 30 days for the purpose of StartedAt derivation (rate
// extrapolation rounds anyway).
//
// timeUnit is matched by *suffix* on the bare unit name because the live
// API returns enum strings prefixed with `TIME_UNIT_`
// ("TIME_UNIT_MINUTE" / "TIME_UNIT_HOUR" / ...) but the upstream reference
// uses bare names ("MINUTE" / "HOUR" / ...). New variants like WEEK land
// in the same way.
func kimiWindowLength(w kimiWindowSpec) (time.Duration, bool) {
	if w.Duration == nil {
		return 0, false
	}
	d := float64(*w.Duration)
	unit := strings.ToUpper(w.TimeUnit)
	unit = strings.TrimPrefix(unit, "TIME_UNIT_")
	switch unit {
	case "MINUTE", "MINUTES", "M":
		return time.Duration(d) * time.Minute, true
	case "HOUR", "HOURS", "H":
		return time.Duration(d) * time.Hour, true
	case "DAY", "DAYS", "D":
		return time.Duration(d) * 24 * time.Hour, true
	case "WEEK", "WEEKS", "W":
		return time.Duration(d) * 7 * 24 * time.Hour, true
	case "MONTH", "MONTHS":
		return time.Duration(d) * 30 * 24 * time.Hour, true
	}
	return 0, false
}

// kimiRowToWindow converts a shape A row (which may have inline or nested
// detail) into a model.Window. Reset fields prefer the row level (shape A
// convention: resetTime rides on the row) but fall back to the detail block
// (shape B convention: resetTime is nested inside limits[].detail).
func kimiRowToWindow(r kimiUsageRow, label string, length time.Duration) *model.Window {
	detail := r.Detail
	if detail == nil && (r.Used != nil || r.Limit != nil || r.Remaining != nil) {
		detail = &kimiWindowDetail{Used: r.Used, Limit: r.Limit, Remaining: r.Remaining}
	}
	if detail == nil {
		return nil
	}
	resetTime := firstNonEmpty(detail.ResetTime, r.ResetTime)
	resetAt := firstNonEmpty(detail.ResetAt, r.ResetAt)
	resetIn := detail.ResetIn
	if resetIn == nil {
		resetIn = r.ResetIn
	}
	return kimiDetailToWindow(*detail, label, length, resetTime, resetAt, resetIn)
}

// kimiDetailToWindow builds a model.Window from a {used, limit, remaining}
// block + reset hints. Length is only used to derive StartedAt (rate
// extrapolation needs it).
func kimiDetailToWindow(d kimiWindowDetail, label string, length time.Duration, resetTime, resetAt string, resetIn *flexFloat) *model.Window {
	pct := remainingFromDetail(d)
	if pct == nil {
		return nil
	}
	w := &model.Window{Label: label}
	rem := *pct
	used := 100 - rem
	w.UsedPct = &used
	w.RemainingPct = &rem
	if length > 0 {
		if t, ok := kimiResolveReset(resetTime, resetAt, resetIn, time.Now()); ok {
			reset := t
			w.ResetAt = &reset
			st := t.Add(-length)
			w.StartedAt = &st
		}
	}
	return w
}

// remainingFromDetail computes remaining% from a {used, limit, remaining}
// block. The Kimi API reports these as absolute counts (not %), so we
// derive the ratio from limit/remaining or limit/used. Returns nil when
// the data is insufficient.
func remainingFromDetail(d kimiWindowDetail) *float64 {
	if d.Limit != nil && *d.Limit > 0 {
		limit := float64(*d.Limit)
		if d.Remaining != nil {
			v := clampPct((float64(*d.Remaining) / limit) * 100)
			return &v
		}
		if d.Used != nil {
			v := clampPct((1 - float64(*d.Used)/limit) * 100)
			return &v
		}
	}
	return nil
}

// kimiResolveReset tries the three reset sources in order: ISO string
// (resetTime/reset_at), then numeric seconds-from-now (reset_in). Returns ok
// false when neither resolves.
func kimiResolveReset(resetTime, resetAt string, resetIn *flexFloat, now time.Time) (time.Time, bool) {
	for _, s := range []string{resetTime, resetAt} {
		if s == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, s); err == nil {
			return t, true
		}
		if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
			return t, true
		}
	}
	if resetIn != nil {
		return now.Add(time.Duration(float64(*resetIn)) * time.Second), true
	}
	return time.Time{}, false
}

func derefFloat(p *flexFloat) float64 {
	if p == nil {
		return 0
	}
	return float64(*p)
}
