---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T11:20:57+00:00"
---

# CHG-20261011T112057Z-e1bf3c4a8fe5 修复 Windows 状态栏重复加载系统时区

## 关联需求

REQ-20261011T092327Z-772c971ad89f E0006;ADR-20261011T111938Z-87667e031e9a。用户授权通过 SSH 直接实机测试。

## 变更内容

配置与生成切换到 pelletier/go-toml/v2 v2.4.3,移除在包初始化读取本地时区的 TOML 依赖。statusline RunE 在读取 stdin/缓存及启动读取 goroutine 前设定进程时区 UTC,避免旧 JSON offset 与新快照再次读取系统时区;高峰提醒继续显式 UTC+8。补充 TOML 1.1、中文、Windows 路径、多行 token、设置往返与旧 offset 缓存兼容测试。Windows 实测暴露两个既有测试问题:复制后的模拟 Git 首次冷加载耗尽预算,新增有界预热;Windows FileMode 不表达 ACL,0600 断言仅保留 Unix。生产 Git 预算未调整。

## 验证结果

先运行配置/CLI 基线,替换后检查兼容;UTC 回归去掉设置后确认失败(新缓存仍为 +08:00),恢复后通过。Linux go test ./...、go vet ./...、相关 race 测试通过;新增测试调整后 go test ./cmd ./internal/secret 与 Windows go vet ./... 通过。Windows 原生 go test ./... 通过。Go 1.26.1 实机 200 次 P50 32.45ms/P95 34.94ms,原版 84.02ms/92.02ms;带采样 500 次全部成功,结束时测试程序与 Git 无残留,OpenConsole/conhost 回到基线。最小 Go 程序 26.52ms/28.54ms,剩余启动成本接近基线。实机原始日志留在远端 E:\Projects\submon\statusline-logs\ssh-diagnostics-20261011 与本地 statusline-logs/ssh-diagnostics-20261011。远端原源码和 submon.exe 未覆盖;短时采样不证明长期无泄漏。
