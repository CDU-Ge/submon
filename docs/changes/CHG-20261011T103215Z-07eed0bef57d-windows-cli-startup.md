---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T10:32:15+00:00"
---

# CHG-20261011T103215Z-07eed0bef57d 关闭 Windows CLI 每次启动的 Explorer 检测

## 关联需求

REQ-20261011T092327Z-772c971ad89f-statusline-resources 的 E0004。

## 变更内容

在根命令初始化时设置 Cobra MousetrapHelpText 为空,跳过 Windows Execute 之前的 StartedByExplorer 进程快照检测。证据来自本地依赖 cobra v1.10.2 command_win.go 与 mousetrap v1.1.0 trap_windows.go。全部命令沿用 CLI 入口,不再提供 Explorer 双击暂停提示。诊断新增 cli_ms,通过命令 context 保存 Execute 入口时间;脚本摘要拆分 cli_ms 和 outside_cli_ms,兼容旧二进制没有 cli_ms 的诊断输出。

## 验证结果

先运行公开 Cobra 配置约束测试,确认 Explorer 检测未禁用而失败,再关闭开关并通过。诊断输出保持 stdout 不变的集成测试通过。临时 Linux 脚本副本仅移除平台门禁,50 次调用的 CLI 平均 0.05ms、剩余外部启动与收尾平均 3.12ms,日志 /tmp/submon-startup-perf-logs。此测量不反映 Windows 优化降幅;仍须用户重建 Windows 二进制后复测。

最终验证:go test ./...、go test -race ./cmd -count=1、go vet ./...、Windows 全包 vet、Windows 主程序构建与 cmd 测试程序编译通过;PowerShell 实际脚本路径回归通过,git diff --check 通过。Windows 实际降幅尚未验证。
