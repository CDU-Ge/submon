package predict

import (
	"testing"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/model"
)

func mkWin(remaining float64, reset time.Time) *model.Window {
	used := 100 - remaining
	return &model.Window{RemainingPct: &remaining, UsedPct: &used, ResetAt: &reset}
}

func TestWindowsUntilRestCoefficient(t *testing.T) {
	now := time.Now()
	// 还剩 2 天 → 2×3 = 6 个窗口。
	got := WindowsUntil(now, now.Add(48*time.Hour))
	if got < 5.9 || got > 6.1 {
		t.Errorf("2 days should give ~6 windows (3/day), got %.1f", got)
	}
	// 还剩半天 → 1.5 个。
	got = WindowsUntil(now, now.Add(12*time.Hour))
	if got < 1.4 || got > 1.6 {
		t.Errorf("12h should give ~1.5 windows, got %.1f", got)
	}
	// 已过期 → 0。
	if got := WindowsUntil(now, now.Add(-time.Hour)); got != 0 {
		t.Errorf("expired should be 0, got %.1f", got)
	}
}

func TestWeeklyFateInevitableWaste(t *testing.T) {
	now := time.Now()
	// weekly 还剩 80%,离重置还有 2 天(=6 个满载窗口)。
	// 历史显示转换率:5h 用 100% 时 weekly 只降 5%(rate=0.05)。
	// 消耗能力 = 6×5% = 30% < 80% → 必然浪费 50%。
	long := mkWin(80, now.Add(48*time.Hour))
	short := &model.Window{Label: "5h"}

	// 构造历史:5h 从 0→50→100(两个满载过程),weekly 对应涨 2.5→5。
	var hist []cache.Sample
	for i, su := range []float64{0, 25, 50, 75, 100} {
		hist = append(hist, cache.Sample{
			T:         now.Add(time.Duration(i) * time.Hour),
			ShortUsed: su,
			LongUsed:  su * 0.05, // 0, 1.25, 2.5, 3.75, 5.0
		})
	}
	res := WeeklyFate(short, long, hist, now)
	if res == nil {
		t.Fatal("nil result")
	}
	if res.MaxBurnPerWindow < 4.9 || res.MaxBurnPerWindow > 5.1 {
		t.Errorf("conversion should be ~5%%/window, got %.1f", res.MaxBurnPerWindow)
	}
	if res.CanAvoid {
		t.Error("80% remaining vs 30% capacity should be inevitable")
	}
	if res.InevitablePct < 49 || res.InevitablePct > 51 {
		t.Errorf("inevitable waste should be ~50, got %.0f", res.InevitablePct)
	}
}

func TestWeeklyFateAvoidable(t *testing.T) {
	now := time.Now()
	// weekly 剩 20%,还有 5 天(15 个窗口),每窗口消耗 5% → 能力 75% > 20%。
	long := mkWin(20, now.Add(5*24*time.Hour))
	res := WeeklyFate(nil, long, nil, now) // 无历史 → 保守 ~3%/窗口
	if res == nil || !res.CanAvoid {
		t.Fatalf("20%% vs 5-day capacity should be avoidable, got %+v", res)
	}
	if !res.Conservative {
		t.Error("no history should set Conservative")
	}
}

func TestConversionRateRejectsBadSamples(t *testing.T) {
	now := time.Now()
	// 噪声样本(重置跳变:used 下降)应被过滤。
	h := []cache.Sample{
		{T: now.Add(-3 * time.Hour), ShortUsed: 10, LongUsed: 1},
		{T: now.Add(-2 * time.Hour), ShortUsed: 20, LongUsed: 2},
		{T: now.Add(-1 * time.Hour), ShortUsed: 90, LongUsed: 80}, // dl=78>50 噪声
	}
	if _, ok := conversionRate(h); ok {
		// 只有 2 个有效样本对 < 3 → 应当不足。
		t.Error("2 valid pairs should be insufficient")
	}
	// 空历史。
	if _, ok := conversionRate(nil); ok {
		t.Error("nil history should be insufficient")
	}
}
