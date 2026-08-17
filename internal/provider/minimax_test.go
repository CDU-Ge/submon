package provider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// Live remains payload shape (truncated to the fields we parse).
const minimaxRemainsPayload = `{
  "model_remains": [{
    "start_time": 1786968000000,
    "end_time": 1786982400000,
    "remains_time": 13034309,
    "current_interval_total_count": 0,
    "current_interval_usage_count": 0,
    "model_name": "general",
    "current_weekly_total_count": 0,
    "current_weekly_usage_count": 0,
    "weekly_remains_time": 531434309,
    "current_interval_remaining_percent": 100,
    "current_weekly_remaining_percent": 99
  }],
  "base_resp": {"status_code": 0, "status_msg": "success"}
}`

func TestMinimaxFetchParsesTopLevelModelRemains(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/api/openplatform/coding_plan/remains" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(minimaxRemainsPayload))
			return
		}
		// Subscription endpoint: optional, return empty object.
		_, _ = w.Write([]byte(`{}`))
	}))
	defer srv.Close()

	m := &MiniMax{Token: "t", BaseURL: srv.URL, Client: srv.Client()}
	snap, err := m.Fetch(context.Background())
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if snap.PlanLevel != "general" {
		t.Errorf("plan level = %q", snap.PlanLevel)
	}
	if snap.ShortWindow == nil || snap.ShortWindow.RemainingPct == nil || *snap.ShortWindow.RemainingPct != 100 {
		t.Fatalf("short window remaining pct, got %+v", snap.ShortWindow)
	}
	if snap.LongWindow == nil || snap.LongWindow.RemainingPct == nil || *snap.LongWindow.RemainingPct != 99 {
		t.Fatalf("weekly remaining pct = %v", snap.LongWindow)
	}
	// remains_time=13034309ms → reset ≈ 3.6h from fetch time.
	if snap.ShortWindow.ResetAt == nil {
		t.Fatal("short window reset time missing")
	}
	if d := time.Until(*snap.ShortWindow.ResetAt); d < 3*time.Hour || d > 4*time.Hour {
		t.Errorf("short window reset in %v, want ~3.6h", d)
	}
}

func TestMinimaxNotConfigured(t *testing.T) {
	m := &MiniMax{Token: "", ConfigPath: "/nonexistent/minimax-config.json"}
	if _, err := m.Fetch(context.Background()); err == nil {
		t.Fatal("expected ErrNotConfigured")
	}
}

func TestMinimaxUnlimitedWeekly(t *testing.T) {
	var resp remainsResponse
	payload := `{"model_remains":[{"model_name":"general","remains_time":1000,
		"current_interval_remaining_percent":50,
		"current_weekly_total_count":0,
		"current_weekly_usage_count":0}],
		"base_resp":{"status_code":0}}`
	if err := json.Unmarshal([]byte(payload), &resp); err != nil {
		t.Fatal(err)
	}
	first := resp.ModelRemains[0]
	unlimited := first.CurrentWeeklyTotalCount == 0 && first.CurrentWeeklyRemainingPercent == nil
	if !unlimited {
		t.Error("weekly total=0 with no pct should count as unlimited")
	}
}
