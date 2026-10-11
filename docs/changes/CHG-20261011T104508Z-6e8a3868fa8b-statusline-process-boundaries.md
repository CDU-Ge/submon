---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T10:45:08+00:00"
---

# CHG-20261011T104508Z-6e8a3868fa8b 区分状态栏初始化与退出等待

## 关联需求

REQ-20261011T092327Z-772c971ad89f-statusline-resources.md E0005。Windows 等待退出平均 82.59ms,现有计时无法区分进入 CLI 前与 RunE 完成后的开销。

## 变更内容

可选诊断输出 CLI 入口与 RunE 完成的 Unix 毫秒时间戳。PowerShell 脚本计算 before_cli_ms 和 after_run_ms,保留逐次日志、均值和终端输出;兼容旧二进制。使用系统墙钟定位跨进程边界,保留精度或校时导致的负值。默认状态栏输出和行为保持不变。

## 验证结果

先扩展诊断回归并确认缺失时间戳失败,实现后 go test ./cmd -count=1、go vet ./cmd、Windows amd64 交叉构建与 PowerShell 路径回归通过。同脚本 Linux 200 次实测 P50 3.10ms/P95 3.69ms,启动边界平均 2.61ms,退出边界 0.22ms。日志保留于 statusline-logs/local-boundaries-20261011T104419Z/,仅测试副本移除 Windows 平台限制。Windows 仍需重新构建并复测,主因尚未确认。
