package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
)

// shapeAPayload mirrors the upstream tool's preferred shape: an "all" row
// (no window) for the weekly aggregate, plus per-model rows with a window.
// Field semantics verified against the upstream reference README.
const kimiShapeAPayload = `{
  "data": [
    {
      "model_name": "all",
      "used": 120,
      "limit": 1000,
      "remaining": 880,
      "resetTime": "2026-08-26T00:00:00Z"
    },
    {
      "model_name": "kimi-k2",
      "window": {"duration": 5, "timeUnit": "HOUR"},
      "used": 45,
      "limit": 200,
      "remaining": 155,
      "resetTime": "2026-08-19T13:00:00Z"
    },
    {
      "model_name": "kimi-k2-thinking",
      "window": {"duration": 5, "timeUnit": "HOUR"},
      "used": 80,
      "limit": 200,
      "remaining": 120,
      "resetTime": "2026-08-19T13:00:00Z"
    }
  ]
}`

// shapeBPayload mirrors the fallback shape: a top-level "usage" object
// (weekly aggregate) and a "limits" array with nested "detail".
const kimiShapeBPayload = `{
  "usage": {
    "used": 50,
    "limit": 1000,
    "remaining": 950,
    "resetTime": "2026-08-26T00:00:00Z"
  },
  "limits": [
    {
      "window": {"duration": 5, "timeUnit": "HOUR"},
      "detail": {"used": 30, "limit": 200, "remaining": 170},
      "resetTime": "2026-08-19T13:00:00Z"
    }
  ]
}`

func TestKimiFetchShapeA(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/usages" {
			t.Errorf("path = %q, want /usages", r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if got := r.Header.Get("User-Agent"); got != "KimiCLI/1.6" {
			t.Errorf("User-Agent = %q, want KimiCLI/1.6", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(kimiShapeAPayload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "test-key", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Provider != model.ProviderKimi {
		t.Errorf("provider = %q", snap.Provider)
	}

	// ShortWindow: 两行 5h,kimi-k2 (155/200 = 77.5%) 与 kimi-k2-thinking (120/200 = 60%);
	// 取最低剩余 60%。
	if snap.ShortWindow == nil {
		t.Fatal("nil short window")
	}
	if snap.ShortWindow.Label != "5h" {
		t.Errorf("short label = %q", snap.ShortWindow.Label)
	}
	if snap.ShortWindow.RemainingPct == nil || *snap.ShortWindow.RemainingPct != 60 {
		t.Errorf("short remaining = %v, want 60", snap.ShortWindow.RemainingPct)
	}
	if snap.ShortWindow.ResetAt == nil {
		t.Error("short ResetAt missing")
	}

	// LongWindow: 从 "all" 行 → 剩余 88%。
	if snap.LongWindow == nil || snap.LongWindow.RemainingPct == nil || *snap.LongWindow.RemainingPct != 88 {
		t.Errorf("long remaining = %v, want 88", snap.LongWindow.RemainingPct)
	}
	if snap.LongWindow.Label != "week" {
		t.Errorf("long label = %q", snap.LongWindow.Label)
	}
}

func TestKimiFetchShapeBFallback(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(kimiShapeBPayload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "test-key", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	// usage → week: 950/1000 = 95%。
	if snap.LongWindow == nil || snap.LongWindow.RemainingPct == nil || *snap.LongWindow.RemainingPct != 95 {
		t.Errorf("long remaining = %v, want 95", snap.LongWindow.RemainingPct)
	}
	// limits[0] 5h: 170/200 = 85%。
	if snap.ShortWindow == nil || snap.ShortWindow.RemainingPct == nil || *snap.ShortWindow.RemainingPct != 85 {
		t.Errorf("short remaining = %v, want 85", snap.ShortWindow.RemainingPct)
	}
}

func TestKimiFetch404Fallback(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		switch r.URL.Path {
		case "/usages":
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte("nope"))
		case "/usage":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(kimiShapeBPayload))
		default:
			t.Errorf("unexpected path %q", r.URL.Path)
		}
	}))
	defer srv.Close()

	k := &Kimi{Token: "test-key", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if hits != 2 {
		t.Errorf("expected 2 hits (/usages + /usage), got %d", hits)
	}
	if snap.LongWindow == nil {
		t.Error("long window missing after fallback")
	}
}

func TestKimiFetchWithMonthlyWindow(t *testing.T) {
	// 30 DAY 月度窗口(WebBridge 会员)→ MonthlyToolWindow。
	payload := `{
	  "data": [
	    {"model_name": "all", "used": 100, "limit": 1000, "remaining": 900, "resetTime": "2026-08-26T00:00:00Z"},
	    {"model_name": "kimi-k2", "window": {"duration": 5, "timeUnit": "HOUR"}, "used": 20, "limit": 200, "remaining": 180, "resetTime": "2026-08-19T13:00:00Z"},
	    {"model_name": "monthly", "window": {"duration": 30, "timeUnit": "DAY"}, "used": 10, "limit": 100, "remaining": 90, "resetTime": "2026-09-18T00:00:00Z"}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "k", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.MonthlyToolWindow == nil {
		t.Fatal("monthly window missing")
	}
	if snap.MonthlyToolWindow.Label != "month" {
		t.Errorf("monthly label = %q", snap.MonthlyToolWindow.Label)
	}
	if snap.MonthlyToolWindow.RemainingPct == nil || *snap.MonthlyToolWindow.RemainingPct != 90 {
		t.Errorf("monthly remaining = %v, want 90", snap.MonthlyToolWindow.RemainingPct)
	}
}

func TestKimiFetchNotConfigured(t *testing.T) {
	// 清空环境变量;os.Getenv 在测试中可能受外部污染,但 token 字段优先于 env。
	t.Setenv("KIMI_CODING_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	k := &Kimi{BaseURL: "http://x"}
	if _, err := k.Fetch(context.Background()); err == nil {
		t.Fatal("expected ErrNotConfigured without token")
	}
}

func TestKimiFetchResetInSeconds(t *testing.T) {
	// reset_in 兜底:减号 offset 解析。
	payload := `{
	  "data": [
	    {"model_name": "all", "used": 5, "limit": 100, "remaining": 95, "reset_in": 86400},
	    {"model_name": "kimi-k2", "window": {"duration": 5, "timeUnit": "HOUR"}, "used": 5, "limit": 100, "remaining": 95, "reset_in": 7200}
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "k", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.LongWindow == nil || snap.LongWindow.ResetAt == nil {
		t.Fatal("missing long window / reset")
	}
	// 24h 后重置,允许 ±1s 抖动。
	delta := time.Until(*snap.LongWindow.ResetAt)
	if delta < 23*time.Hour || delta > 25*time.Hour {
		t.Errorf("weekly reset delta = %v, want ~24h", delta)
	}
}

func TestKimiBuildAndDetect(t *testing.T) {
	t.Setenv("KIMI_CODING_API_KEY", "env-token")
	cfg := config.Default()
	cfg.Kimi.Token = "cfg-token"
	src, err := Build(model.ProviderKimi, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if src.Name() != model.ProviderKimi {
		t.Errorf("Name = %q", src.Name())
	}
	if _, ok := src.(*Kimi); !ok {
		t.Errorf("Build returned %T, want *Kimi", src)
	}

	// env > cfg
	if k := src.(*Kimi); k.Token != "env-token" {
		t.Errorf("token = %q, want env-token", k.Token)
	}

	// env > cfg: 清掉 env,应回到 cfg。
	t.Setenv("KIMI_CODING_API_KEY", "")
	t.Setenv("KIMI_API_KEY", "")
	src2, err := Build(model.ProviderKimi, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if k := src2.(*Kimi); k.Token != "cfg-token" {
		t.Errorf("token = %q, want cfg-token", k.Token)
	}

	// DetectFromBaseURL: kimi.com 与 moonshot 都识别。
	if got, err := DetectFromBaseURL("https://api.kimi.com/coding/v1"); err != nil || got != model.ProviderKimi {
		t.Errorf("DetectFromBaseURL(kimi.com) = %v, %v", got, err)
	}
	if got, err := DetectFromBaseURL("https://api.moonshot.cn/v1"); err != nil || got != model.ProviderKimi {
		t.Errorf("DetectFromBaseURL(moonshot.cn) = %v, %v", got, err)
	}
}

// TestKimiFetchEmptyPayload:避免错误地接受 {} 作为合法响应。
func TestKimiFetchEmptyPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer srv.Close()

	k := &Kimi{Token: "k", BaseURL: srv.URL}
	if _, err := k.Fetch(context.Background()); err == nil {
		t.Fatal("expected error on empty payload")
	}
}

// helper 包内使用,放在 kimi_test.go 末尾避免污染外部包。
func TestKimiHealthcheckJSONShape(t *testing.T) {
	// 防止 shape A / shape B 在重构里被改坏 — 起码确保默认 mock 仍能解出预期字段。
	var a kimiResponseA
	if err := json.Unmarshal([]byte(kimiShapeAPayload), &a); err != nil {
		t.Fatalf("shape A parse: %v", err)
	}
	if len(a.Data) != 3 {
		t.Errorf("shape A rows = %d, want 3", len(a.Data))
	}
	var b kimiResponseB
	if err := json.Unmarshal([]byte(kimiShapeBPayload), &b); err != nil {
		t.Fatalf("shape B parse: %v", err)
	}
	if b.Usage == nil || len(b.Limits) != 1 {
		t.Errorf("shape B: usage=%v, limits=%d", b.Usage, len(b.Limits))
	}
}
