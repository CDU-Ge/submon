package provider

import (
	"testing"
)

// Live quota/limit payload captured 2026-08-17.
func TestZhipuParseLimitWindows(t *testing.T) {
	z := &Zhipu{}
	used := 1.0
	reset := int64(1786985202759)

	short, kind, err := z.parseLimit(quotaLimit{Type: "TOKENS_LIMIT", Unit: 3, Number: 5, Percentage: &used, NextResetTime: &reset})
	if err != nil || kind != "short" {
		t.Fatalf("short window: kind=%q err=%v", kind, err)
	}
	if short.RemainingPct == nil || *short.RemainingPct != 99 {
		t.Errorf("remaining should flip used%% → 99, got %v", short.RemainingPct)
	}
	if short.ResetAt == nil || short.ResetAt.UnixMilli() != reset {
		t.Errorf("reset time not carried: %v", short.ResetAt)
	}

	long, kind, err := z.parseLimit(quotaLimit{Type: "TOKENS_LIMIT", Unit: 6, Number: 1, Percentage: &used})
	if err != nil || kind != "long" || long.Label != "week" {
		t.Fatalf("weekly window: kind=%q label=%q err=%v", kind, long.Label, err)
	}

	// TIME_LIMIT carries counts: usage=1000 total, currentValue=0 used → 1000 remaining.
	total := int64(1000)
	usedCount := int64(0)
	monthly, kind, err := z.parseLimit(quotaLimit{Type: "TIME_LIMIT", Unit: 5, Number: 1, Usage: &total, CurrentValue: &usedCount, Remaining: &total})
	if err != nil || kind != "monthly_tool" {
		t.Fatalf("monthly window: kind=%q err=%v", kind, err)
	}
	if monthly.RemainingCount == nil || *monthly.RemainingCount != 1000 {
		t.Errorf("monthly remaining count, got %v", monthly.RemainingCount)
	}

	// Unknown combos must surface, not silently mislabel.
	if _, _, err := z.parseLimit(quotaLimit{Type: "TOKENS_LIMIT", Unit: 9, Number: 2}); err == nil {
		t.Error("unknown unit/number combo should error")
	}
}

func TestZhipuPctClamp(t *testing.T) {
	for _, c := range []struct{ in, want float64 }{{-3, 0}, {50, 50}, {140, 100}} {
		if got := clampPct(c.in); got != c.want {
			t.Errorf("clampPct(%v) = %v, want %v", c.in, got, c.want)
		}
	}
}
