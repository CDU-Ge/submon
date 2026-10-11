---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T09:53:29+00:00"
---

# CHG-20261011T095329Z-8a3714bf6697 分离状态栏调用耗时与压测开销

## 关联需求

REQ-20261011T092327Z-772c971ad89f-statusline-resources 的 E0003。

## 变更内容

修正 duration_ms,在进程退出并排空输出后停止计时,退出后内存查询单独记录并改为可选。脚本分解启动、input、等待退出、输出收尾、内存查询耗时,单独报告准备、循环、等待间隔和尾部采样,新增固定容量最近 10000 次 P50/P95。-NoProcessSampling 可隔离采样影响;-TraceStages 通过子进程环境启用 config/stdin/render/run 的 stderr 诊断,不会改变 stdout。未对尚未定位的 Windows 程序瓶颈做推测性优化。

## 验证结果

读取原有 Linux 日志:100 次单次中位数约 2.94ms,整次脚本约 3.50s,含固定 3 秒收尾。临时 Linux 副本仅移除平台门禁,新脚本 52 次默认采样和间隔时循环约 0.81s,尾部约 3.05s;无采样/间隔时循环约 0.28s,P50 3.26ms/P95 4.07ms,日志保留于 /tmp/submon-stress-perf-nosampling-logs。诊断输出不改变 stdout 的 CLI 集成测试通过。本次诊断代码和集成测试在同轮编写,非先失败再实现。尚未取得用户 Windows 单次/分段耗时。

最终检查:go test ./cmd -count=1、go test -race ./cmd -count=1、go vet ./cmd、Windows cmd vet、主程序构建与 cmd 测试程序编译通过,PowerShell 语法解析通过。开启单次内存查询的 20 次循环与无采样超时案例通过;JSONL 校验 duration_ms 等于启动/input/等待退出/输出收尾四段之和,摘要调用时间总和正确,诊断分段齐全,日志保留在 /tmp/submon-stress-perf-metrics-logs 和 /tmp/submon-stress-perf-timeout-logs。
