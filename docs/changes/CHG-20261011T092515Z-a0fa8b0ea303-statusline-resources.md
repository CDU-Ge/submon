---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T09:25:15+00:00"
---

# CHG-20261011T092515Z-a0fa8b0ea303 消除状态栏后台刷新进程放大

## 关联需求

REQ-20261011T092327Z-772c971ad89f-statusline-resources; ADR-20261011T092328Z-52e4d47afda6-statusline-refresh。

## 变更内容

移除 maybeSelfHeal 和脱离父进程的 watch 启动路径,保留 watch --quiet 的显式调用兼容。Git 查询限定 150ms,禁用 fsmonitor,输出管道收尾限定 25ms;查询前持久化尝试时间,失败保留旧值并缓存 5 秒,缓存不可写时跳过。内核非阻塞锁替代按年龄删除的文件锁。stdin 关闭可关闭的读取端并取消发送,停止计时器,限制输入为 1 MiB。三种布局验证配额缓存只读,布局 B 补充新鲜度尾标。同步 README、CLAUDE,新增 GLOSSARY 与 DEVELOPMENT。

## 验证结果

先运行缺失缓存、锁年龄和 Git 失败回归,确认三项暴露旧实现缺陷后修复。stdin 清理测试先失败再修复;过期数据三布局测试暴露 B 缺少新鲜度提示,随后补齐。go test ./...、go vet ./... 通过;最终 go test -race ./cmd -count=1、go vet ./cmd、GOOS=windows go vet ./cmd 通过;Windows 全包 vet、主程序构建和 cmd 测试程序交叉编译通过。git diff --check 通过。Linux 实际 CLI 8 路并发共 300 次,中位数 4.0ms、P95 5.6ms、最大 7.6ms,无配额写入与刷新锁。卡住的 Git 超时后 100 次调用仅一次 spawn,持锁进程强制终止后锁可重新取得。Windows 构建产物 /tmp/submon-statusline-windows.exe;未执行 Windows 实机测试,未确认用户设备的实际内存泄漏根因。

### 补充变更与验证

移除 --provider、--show-provider 与 SUBMON_SHOW_PROVIDER;status 面板标题保留 provider,安装命令清理旧 statusline 参数。新增 Windows Job Object 清理与父进程强制终止回归。新增 scripts/statusline-stress.ps1 和 docs/statusline-stress.md,兼容 PowerShell 5.1/7,记录执行输出、PID、内存、句柄及基线/峰值/结束摘要,自动超时和增长保护,默认日志目录保留本地并加入 Git 忽略规则。

参数移除与旧设置迁移测试先失败再修复。补充后 go test ./...、go test -race ./cmd -count=1、go vet ./...、Windows 全包 vet 与测试程序编译通过。使用临时下载的 PowerShell 7.4.6 执行语法解析通过;临时 Linux 测试副本仅移除 Windows 平台门禁,实际主程序循环 100 次全部成功,生成 33 条独立采样记录,JSONL 与摘要结构检查通过。故意卡住的子进程触发超时,仅调用一次便停止、退出码 1,日志与摘要保留。Windows 专有 Job Object 与内存行为尚未实机执行。已有 Linux 300 次性能记录为移除参数前版本,不作为最终 Windows 性能结论。

最终脚本改用 .NET 枚举进程,避免不存在的进程名称产生并累积采样器 ErrorRecords;加入 UTF-8 BOM 以支持 Windows PowerShell 中文文案。最终 Linux 冒烟循环仍为 100 次成功、33 次独立采样,日志保留在 /tmp/submon-stress-final-logs;超时案例日志保留在 /tmp/submon-stress-timeout-logs。最终全量测试、CLI race、两平台 vet、Windows 构建及测试程序编译通过。
