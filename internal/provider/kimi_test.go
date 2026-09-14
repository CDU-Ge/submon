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
	cfg.Accounts = []config.Account{{Provider: "kimi", Label: "default", Token: "cfg-token"}}
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
	if got, err := DetectFromBaseURL("https://api.kimi.com/coding/v1", nil); err != nil || got != model.ProviderKimi {
		t.Errorf("DetectFromBaseURL(kimi.com) = %v, %v", got, err)
	}
	if got, err := DetectFromBaseURL("https://api.moonshot.cn/v1", nil); err != nil || got != model.ProviderKimi {
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

// TestKimiFetchShapeBStringLimit 回归真实 API:同一字段在不同端点交替出
// string / number。用户报告的错误是 "kimiUsageRow.usage.limit of type
// float64" — 该路径只可能来自 shape B 的 Usage 字段,所以这个 fixture 是
// 直接覆盖用户报错形状。
func TestKimiFetchShapeBStringLimit(t *testing.T) {
	payload := `{
	  "usage": {
	    "used": "50",
	    "limit": "1000",
	    "remaining": "950",
	    "resetTime": "2026-08-26T00:00:00Z"
	  },
	  "limits": [
	    {
	      "window": {"duration": "5", "timeUnit": "HOUR"},
	      "detail": {"used": "30", "limit": "200", "remaining": "170"},
	      "resetTime": "2026-08-19T13:00:00Z"
	    }
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "k", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatalf("string-typed limit rejected: %v", err)
	}
	if snap.LongWindow == nil || snap.LongWindow.RemainingPct == nil || *snap.LongWindow.RemainingPct != 95 {
		t.Errorf("long remaining = %v, want 95", snap.LongWindow.RemainingPct)
	}
	if snap.ShortWindow == nil || snap.ShortWindow.RemainingPct == nil || *snap.ShortWindow.RemainingPct != 85 {
		t.Errorf("short remaining = %v, want 85", snap.ShortWindow.RemainingPct)
	}
}

// TestKimiFetchShapeAStringNumber 混合形态:行内是 number,nested detail 是 string
// (或反之),不能因为某一行 string 就让整个 payload 崩。
func TestKimiFetchShapeAMixedNumberString(t *testing.T) {
	payload := `{
	  "data": [
	    {
	      "model_name": "all",
	      "used": 100,
	      "limit": 1000,
	      "remaining": 900,
	      "resetTime": "2026-08-26T00:00:00Z"
	    },
	    {
	      "model_name": "kimi-k2",
	      "window": {"duration": 5, "timeUnit": "HOUR"},
	      "used": "40",
	      "limit": "200",
	      "remaining": "160",
	      "reset_in": "7200"
	    }
	  ]
	}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(payload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "k", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatalf("mixed string/number payload rejected: %v", err)
	}
	if snap.LongWindow == nil || *snap.LongWindow.RemainingPct != 90 {
		t.Errorf("long remaining = %v, want 90", snap.LongWindow.RemainingPct)
	}
	if snap.ShortWindow == nil || *snap.ShortWindow.RemainingPct != 80 {
		t.Errorf("short remaining = %v, want 80", snap.ShortWindow.RemainingPct)
	}
	if snap.ShortWindow.ResetAt == nil {
		t.Error("short ResetAt missing — reset_in should still resolve")
	}
}

// TestFlexFloatUnit 直接验证 UnmarshalJSON 同时吃 number / string。
func TestFlexFloatUnit(t *testing.T) {
	var f flexFloat
	if err := json.Unmarshal([]byte(`42`), &f); err != nil {
		t.Fatalf("number: %v", err)
	}
	if float64(f) != 42 {
		t.Errorf("number decoded = %v, want 42", float64(f))
	}
	if err := json.Unmarshal([]byte(`"3.14"`), &f); err != nil {
		t.Fatalf("string: %v", err)
	}
	if float64(f) != 3.14 {
		t.Errorf("string decoded = %v, want 3.14", float64(f))
	}
	if err := json.Unmarshal([]byte(`"not-a-number"`), &f); err == nil {
		t.Error("garbage string should fail")
	}
	if err := json.Unmarshal([]byte(`true`), &f); err == nil {
		t.Error("bool should fail")
	}
}

// kimiRealAPIPayload 来自 kimi-usage-probe 实跑,2026-08-19 抓取:
// api.kimi.com/coding/v1/usages 真实响应,数字全是 string、timeUnit 用
// "TIME_UNIT_MINUTE" 前缀、并附带 user / parallel / totalQuota /
// authentication / subType / domain 等额外块(Go json 默认忽略未知字段)。
//
// 这个 fixture 守住三个回归:
//  1. flexFloat 必须吃 string
//  2. kimiWindowLength 必须认 "TIME_UNIT_MINUTE" 前缀(没这条修过的话,
//     整个 5h 窗口会被丢,只剩 weekly 0%)
//  3. 额外块不能导致 unmarshal 失败
const kimiRealAPIPayload = `{
  "user": {
    "userId": "d9vh9dnaa0v22ffb9r4g",
    "region": "REGION_CN",
    "membership": {"level": "LEVEL_INTERMEDIATE"},
    "businessId": ""
  },
  "usage": {
    "limit": "100",
    "used": "100",
    "resetTime": "2026-08-22T06:27:15.950634Z"
  },
  "limits": [
    {
      "window": {"duration": 300, "timeUnit": "TIME_UNIT_MINUTE"},
      "detail": {
        "limit": "100",
        "used": "44",
        "remaining": "56",
        "resetTime": "2026-08-19T15:27:15.950634Z"
      }
    }
  ],
  "parallel": {"limit": "20"},
  "totalQuota": {},
  "authentication": {"method": "METHOD_API_KEY", "scope": "FEATURE_CODING"},
  "subType": "TYPE_PURCHASE",
  "domain": "DOMAIN_NEXUS"
}`

// TestKimiFetchRealAPIPayload 是用户报告错误的直接 regression。
// 不 mock:用真 payload 直接走 Fetch,期望:
//   - shape B 解析成功(usage → week,limits[0] → 5h)
//   - short 窗口 56% remaining(关键:kimiWindowLength 要认 TIME_UNIT_MINUTE)
//   - long 窗口 0% remaining(used == limit)
//   - 两段都拿到 resetTime(没 timeUnit 解析这一步就崩了)
func TestKimiFetchRealAPIPayload(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(kimiRealAPIPayload))
	}))
	defer srv.Close()

	k := &Kimi{Token: "k", BaseURL: srv.URL}
	snap, err := k.Fetch(context.Background())
	if err != nil {
		t.Fatalf("real API payload rejected: %v", err)
	}

	// 5h:detail.remaining / detail.limit = 56/100 → 56%
	// 5h:detail.remaining / detail.limit = 56/100 → 56%
	if snap.ShortWindow == nil {
		t.Fatal("short window missing — TIME_UNIT_MINUTE 没被认")
	}
	if snap.ShortWindow.Label != "5h" {
		t.Errorf("short label = %q, want 5h", snap.ShortWindow.Label)
	}
	if snap.ShortWindow.RemainingPct == nil || !floatNear(*snap.ShortWindow.RemainingPct, 56, 0.001) {
		t.Errorf("short remaining = %v, want 56", snap.ShortWindow.RemainingPct)
	}
	if snap.ShortWindow.ResetAt == nil {
		t.Error("short ResetAt missing — detail.resetTime 没传到 window")
	}

	// week:usage.used == usage.limit → 0%
	if snap.LongWindow == nil {
		t.Fatal("long window missing")
	}
	if snap.LongWindow.Label != "week" {
		t.Errorf("long label = %q, want week", snap.LongWindow.Label)
	}
	if snap.LongWindow.RemainingPct == nil || !floatNear(*snap.LongWindow.RemainingPct, 0, 0.001) {
		t.Errorf("long remaining = %v, want 0 (used==limit)", snap.LongWindow.RemainingPct)
	}
	if snap.LongWindow.ResetAt == nil {
		t.Error("long ResetAt missing — usage.resetTime 没被解析")
	}
}

// floatNear 容忍 float64 路径里的精度抖动(56/100*100 = 56.00000000000001)。
func floatNear(a, b, tol float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d <= tol
}

// TestKimiWindowLengthUnits 单独守住 enum 字符串解析,与真 payload 解耦。
func TestKimiWindowLengthUnits(t *testing.T) {
	mk := func(d *flexFloat, u string) kimiWindowSpec {
		return kimiWindowSpec{Duration: d, TimeUnit: u}
	}
	d := flexFloat(5)
	cases := []struct {
		unit string
		want time.Duration
		ok   bool
	}{
		{"TIME_UNIT_MINUTE", 5 * time.Minute, true},
		{"TIME_UNIT_HOUR", 5 * time.Hour, true},
		{"TIME_UNIT_DAY", 5 * 24 * time.Hour, true},
		{"TIME_UNIT_WEEK", 5 * 7 * 24 * time.Hour, true},
		{"TIME_UNIT_MONTH", 5 * 30 * 24 * time.Hour, true},
		// 旧 enum / 裸名也认(防止上游换前缀)。
		{"MINUTE", 5 * time.Minute, true},
		{"HOUR", 5 * time.Hour, true},
		{"DAY", 5 * 24 * time.Hour, true},
		{"WEEK", 5 * 7 * 24 * time.Hour, true},
		{"MONTH", 5 * 30 * 24 * time.Hour, true},
		// 大小写不敏感。
		{"time_unit_minute", 5 * time.Minute, true},
		{"HOURS", 5 * time.Hour, true},
		// 未知 enum → ok=false,不静默给一个错误的 length。
		{"TIME_UNIT_FORTNIGHT", 0, false},
	}
	for _, c := range cases {
		got, ok := kimiWindowLength(mk(&d, c.unit))
		if ok != c.ok || got != c.want {
			t.Errorf("%s: got (%v, %v) want (%v, %v)", c.unit, got, ok, c.want, c.ok)
		}
	}
}
