package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Payload shape from the user's live capture (2026-08-18):
//
//	{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"..."},
//	          "weekly":{"status":"ok","percent":0,"resetsAt":"..."},
//	          "monthly":{"status":"ok","percent":0,"resetsAt":"..."}}}
//
// percent is *used* percent; resetsAt is RFC3339 with millis.
const opencodeUsagePayload = `{
  "usage": {
    "rolling":  {"status": "ok", "percent": 25, "resetsAt": "2026-08-18T13:21:35.791Z"},
    "weekly":   {"status": "ok", "percent": 36, "resetsAt": "2026-08-24T00:00:00.791Z"},
    "monthly":  {"status": "ok", "percent": 10, "resetsAt": "2026-09-18T07:22:07.791Z"}
  }
}`

func TestOpenCodeFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Authorization"); got != "Bearer test-key" {
			t.Errorf("Authorization = %q, want Bearer test-key", got)
		}
		if r.URL.Path != "/zen/go/v1/usage" {
			t.Errorf("path = %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(json.RawMessage(opencodeUsagePayload))
	}))
	defer srv.Close()

	o := &OpenCode{Token: "test-key", BaseURL: srv.URL}
	snap, err := o.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.Provider != "opencode" {
		t.Errorf("provider = %q", snap.Provider)
	}

	// rolling → short window;percent 是已用%,归一为剩余 75%。
	if snap.ShortWindow == nil {
		t.Fatal("nil short window")
	}
	if snap.ShortWindow.Label != "5h" {
		t.Errorf("short label = %q", snap.ShortWindow.Label)
	}
	if snap.ShortWindow.RemainingPct == nil || *snap.ShortWindow.RemainingPct != 75 {
		t.Errorf("short remaining = %v, want 75", snap.ShortWindow.RemainingPct)
	}
	if snap.ShortWindow.ResetAt == nil {
		t.Error("short ResetAt missing")
	}
	if snap.ShortWindow.StartedAt == nil {
		t.Error("short StartedAt missing (derived as reset − 5h)")
	}

	// weekly → long window,剩余 64%。
	if snap.LongWindow == nil || snap.LongWindow.RemainingPct == nil || *snap.LongWindow.RemainingPct != 64 {
		t.Errorf("long remaining = %v, want 64", snap.LongWindow.RemainingPct)
	}
	// monthly → MonthlyToolWindow,剩余 90%。
	if snap.MonthlyToolWindow == nil || snap.MonthlyToolWindow.RemainingPct == nil ||
		*snap.MonthlyToolWindow.RemainingPct != 90 {
		t.Errorf("monthly remaining = %v, want 90", snap.MonthlyToolWindow.RemainingPct)
	}
}

func TestOpenCodeEmptyPlan(t *testing.T) {
	// 全 0(未使用)也必须有窗口:剩余 100%,zero-usage 提醒逻辑依赖它。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"usage":{"rolling":{"status":"ok","percent":0,"resetsAt":"2026-08-18T13:21:35.791Z"},
			"weekly":{"status":"ok","percent":0,"resetsAt":"2026-08-24T00:00:00.791Z"},
			"monthly":{"status":"ok","percent":0,"resetsAt":"2026-09-18T07:22:07.791Z"}}}`))
	}))
	defer srv.Close()

	o := &OpenCode{Token: "k", BaseURL: srv.URL}
	snap, err := o.Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if snap.ShortWindow == nil || snap.ShortWindow.RemainingPct == nil || *snap.ShortWindow.RemainingPct != 100 {
		t.Errorf("short remaining = %v, want 100", snap.ShortWindow.RemainingPct)
	}
	if snap.LongWindow == nil || snap.LongWindow.RemainingPct == nil || *snap.LongWindow.RemainingPct != 100 {
		t.Errorf("long remaining = %v, want 100", snap.LongWindow.RemainingPct)
	}
}

func TestOpenCodeNotConfigured(t *testing.T) {
	o := &OpenCode{BaseURL: "http://x"}
	if _, err := o.Fetch(context.Background()); err == nil {
		t.Fatal("expected ErrNotConfigured without token")
	}
}
