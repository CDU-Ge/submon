// Package predict 从历史采样预估配额消耗,回答"weekly 剩余是否注定浪费":
//
// 人每天最多活跃 ~15h = 3 个 5h 窗口(要休息)。到 weekly 重置前,
// 实际还能用满载 5h 窗口的数量有限。每个满载 5h 窗口会消耗的
// weekly 百分比可以从历史采样估出(5h 用掉 X% 时 weekly 同时降 Y%,
// 转换率 ≈ Y/X)。若:
//
//	weekly剩余% > 剩余满载窗口数 × 每窗口消耗的weekly%
//
// 则无论怎么用都浪费,差额部分即"必然浪费"。
package predict

import (
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/model"
)

// ActiveWindowsPerDay 人每天实际可投入的 5h 窗口数(扣除休息)。
const ActiveWindowsPerDay = 3

// FiveHourWindow 5h 窗口长度。
const FiveHourWindow = 5 * time.Hour

// Result 是 weekly 必然浪费的评估结果。
type Result struct {
	// CanAvoid 为 false 表示无法避免浪费(必然浪费场景)。
	CanAvoid bool
	// InevitablePct: 必然浪费的 weekly 百分比(0 = 可避免)。
	InevitablePct float64
	// MaxBurnPerWindow: 每个满载 5h 窗口最多消耗的 weekly%(转换率估计)。
	MaxBurnPerWindow float64
	// WindowsLeft: 到 weekly 重置前还能用满载 5h 窗口的数量(考虑休息)。
	WindowsLeft float64
	// Conversion 基于多少个历史样本(0 = 无历史,用了保守默认)。
	Samples int
	// Conservative 为 true 表示 MaxBurnPerWindow 用的是保守上限而非历史估计。
	Conservative bool
}

// conversionRate 从历史估"每消耗 1% 5h 配额,weekly 同步消耗多少%"。
// 返回 0 表示样本不足。用 Δlong/Δshort 的中位数(对突发使用鲁棒)。
// 上限 1.0:5h 用 1% 时 weekly 最多也降 1%(两家窗口刻度可能不同,
// 但超过 1 说明数据噪声,按 1 截断保守处理)。
func conversionRate(h []cache.Sample) (rate float64, ok bool) {
	type pair struct{ ds, dl float64 }
	var ps []pair
	for i := 1; i < len(h); i++ {
		ds := h[i].ShortUsed - h[i-1].ShortUsed
		dl := h[i].LongUsed - h[i-1].LongUsed
		// 只取"两者同向增长且都在累积"的样本对,排除重置跳变与噪声。
		if ds > 0.5 && dl > 0 && dl < 50 {
			ps = append(ps, pair{ds, dl})
		}
	}
	if len(ps) < 3 {
		return 0, false
	}
	rates := make([]float64, len(ps))
	for i, p := range ps {
		rates[i] = p.dl / p.ds
	}
	// 中位数。
	for i := 1; i < len(rates); i++ {
		for j := i; j > 0 && rates[j] < rates[j-1]; j-- {
			rates[j], rates[j-1] = rates[j-1], rates[j]
		}
	}
	med := rates[len(rates)/2]
	if med <= 0 || med > 1 {
		return 0, false
	}
	return med, true
}

// WindowsUntil 到 reset 为止还能投入的满载 5h 窗口数(每日上限 3)。
// 例:还剩 1.2 天 → 3.6 个窗口。部分窗口按比例计。
func WindowsUntil(now time.Time, reset time.Time) float64 {
	d := reset.Sub(now)
	if d <= 0 {
		return 0
	}
	days := d.Hours() / 24
	// 向上取半个窗口余量:现在开始的半个窗口也算投入能力。
	return days * ActiveWindowsPerDay
}

// WeeklyFate 评估 weekly 窗口的命运。
//
// short 是当前 5h 窗口(提供历史换算锚点),long 是 weekly 窗口,
// h 是该 provider 的历史采样。noHistory 时用保守假设:一个满载 5h
// 窗口最多消耗 weekly 的 5/168 ≈ 3%(时间占比即消耗占比上限)。
func WeeklyFate(short, long *model.Window, h []cache.Sample, now time.Time) *Result {
	if long == nil || long.RemainingPct == nil || long.ResetAt == nil {
		return nil
	}
	rate, ok := conversionRate(h)
	res := &Result{Samples: len(h)}
	if ok {
		res.MaxBurnPerWindow = rate * 100 // 满载 5h(=100% 5h 额度)消耗的 weekly%
	} else {
		// 无历史:按时间占比保守估计 — 5h/168h ≈ 2.98%。
		res.MaxBurnPerWindow = 100.0 * FiveHourWindow.Hours() / (7 * 24)
		res.Conservative = true
	}
	if res.MaxBurnPerWindow <= 0 {
		res.MaxBurnPerWindow = 100.0 * FiveHourWindow.Hours() / (7 * 24)
		res.Conservative = true
	}
	res.WindowsLeft = WindowsUntil(now, *long.ResetAt)

	// 可用窗口的总消耗能力 vs 剩余。
	capacity := res.WindowsLeft * res.MaxBurnPerWindow
	remaining := *long.RemainingPct
	if capacity >= remaining {
		res.CanAvoid = true
		res.InevitablePct = 0
		return res
	}
	res.CanAvoid = false
	res.InevitablePct = remaining - capacity
	return res
}
