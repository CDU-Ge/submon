---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T12:08:28+00:00"
---

# CHG-20261011T120828Z-3afe7a8c5e51 部署轻量 Windows 状态栏并核对防护开销

## 关联需求

REQ-20261011T092327Z-772c971ad89f 的 E0008;[Windows 独立一次性状态栏组件 ADR](../adr/ADR-20261011T120828Z-2acb91bea879-windows-statusline-component.md)。

## 变更内容

共享状态栏核心与资源管理,抽取纯路由和展示规则;Windows GUI helper 内嵌 console CLI,install 原子部署并迁移旧命令,修复带 --no-color 的重复安装改写。新增 Windows 构建脚本与隔离用户配置的安装集成测试;保留现有普通 CLI 和自定义状态栏。Defender 性能记录及远端压力日志均保留,未改系统防护或用户自定义配置。

## 验证结果

TDD 覆盖部署内容替换/重复不改写、命令迁移与重复安装;go test ./...、go vet ./... 通过;go test -race ./cmd ./internal/statusline ./internal/routing ./internal/config -count=1 通过;Windows vet 与 macOS arm64 编译通过。Windows Go 1.26.1 原生全量测试通过,最新 cmd 回归通过,PowerShell 安装集成与 Git Bash 输出通过。独立组件 200 次 P50 17.33ms/P95 18.98ms,500 次采样 P50 17.66ms/P95 19.56ms,结束时 submon/Git 无残留。Defender 报告不支持其为固定启动延迟主因,电脑管家扫描较多但未证明卡顿因果。详细日志与限制见 docs/statusline-stress.md。

补充实机验证:5000 次连续调用全部成功,P50 17.79ms/P95 19.77ms,861 次采样结束时 submon/Git 无残留,OpenConsole/conhost 数量回到基线。追踪组 private bytes 增加约 14.8MiB,主要为 PowerShell;未证明长期无泄漏。测试期间并行 Windows Go 回归,峰值包含额外负载。最终重新构建并再次验证隔离安装,日志均已复制本地。
