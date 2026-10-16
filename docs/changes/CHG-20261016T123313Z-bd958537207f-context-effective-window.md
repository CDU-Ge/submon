---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-16T12:33:13+00:00"
---

# CHG-20261016T123313Z-bd958537207f 修正有效上下文窗口进度换算

## 关联需求

docs/requirements/REQ-20261016T123304Z-bd62ddacc398-context-effective-window.md

## 变更内容

ctxUsage 将上游按原始 context_window_size 计算的 used_percentage 换算至现有规则选出的有效窗口；三种布局共用。保留百分比截断和瞬时归零缓存。未增加对 /autocompact 会话设置的读取，窗口仍来自 stdin 和现有环境变量。

## 验证结果

先运行新回归 TestCtxUsageScalesToEffectiveWindow，确认四个缩小窗口案例失败；实现后 go test ./internal/statusline ./cmd -count=1 通过。覆盖压缩窗口、最大窗口覆盖、两者最小值、超限截断、较大窗口不扩张以及瞬时归零。gofmt 与 git diff --check 通过。
