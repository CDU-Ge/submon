package model

import (
	"testing"
	"time"
)

func mkWindow(remaining, used float64, started, reset time.Time) *Window {
	return &Window{
		RemainingPct: &remaining,
		UsedPct:      &used,
		StartedAt:    &started,
		ResetAt:      &reset,
	}
}

func TestPredictedWastePct(t *testing.T) {
	now := time.Now()

	// 案例:5h 窗口已过 4h,用了 20%,剩 80%。
	// rate=5%/h → 剩 1h 还会用 5% → 浪费 80-5=75%。
	w := mkWindow(80, 20, now.Add(-4*time.Hour), now.Add(1*time.Hour))
	got := w.PredictedWastePct(now)
	if got == nil || *got != 75 {
		t.Errorf("want 75%% waste, got %v", got)
	}

	// 案例:快速使用中,预测会用完 → 浪费 0。
	// 已过 1h 用了 90%,剩 1h → 预计再用 90% > 剩 10% → 0。
	w = mkWindow(10, 90, now.Add(-1*time.Hour), now.Add(1*time.Hour))
	got = w.PredictedWastePct(now)
	if got == nil || *got != 0 {
		t.Errorf("fast usage should clamp to 0, got %v", got)
	}

	// 极端值:零使用 → nil(调用方单独处理)。
	w = mkWindow(100, 0, now.Add(-4*time.Hour), now.Add(1*time.Hour))
	if got := w.PredictedWastePct(now); got != nil {
		t.Errorf("zero usage should be nil, got %v", got)
	}

	// 极端值:窗口刚开始(<5min)→ nil。
	w = mkWindow(100, 50, now.Add(-1*time.Minute), now.Add(5*time.Hour))
	if got := w.PredictedWastePct(now); got != nil {
		t.Errorf("fresh window should be nil, got %v", got)
	}

	// 缺字段 → nil。
	w = &Window{RemainingPct: ptr(50.0)}
	if got := w.PredictedWastePct(now); got != nil {
		t.Errorf("missing fields should be nil, got %v", got)
	}
}

func ptr(v float64) *float64 { return &v }
