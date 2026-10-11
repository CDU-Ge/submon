---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T11:19:38+00:00"
---

# ADR-20261011T111938Z-87667e031e9a 状态栏避免系统本地时区初始化

## 决定

状态:采用。配置 TOML 解析和生成统一使用 pelletier/go-toml/v2 v2.4.3。一次性 statusline 在 RunE 入口且读取数据及启动 goroutine 前将进程 time.Local 设为 UTC;其他命令保持系统时区。时间戳仅处理绝对时刻,高峰提醒显式用 UTC+8,旧 JSON offset 保留原时刻,新快照写 UTC。

## 依据与影响

关联 REQ-20261011T092327Z-772c971ad89f E0006。Windows inittrace 证明 BurntSushi/toml/internal 包初始化读取本地时区约 49ms;只换库后相同开销转移到旧缓存时间解析。替换无系统时区 eager 初始化的 TOML 1.1 编解码器并固定状态栏进程时区,同机器 P50 从约 80ms 降至 30.47ms,接近最小 Go 程序 26.52ms。避免维护第三方库补丁或给高频状态栏新增守护进程。配置输出的引号与缩进可能变化,字段及已有语法由兼容性测试验证。time.Local 为进程全局状态,约束仅适用于一次性 CLI;后续若把状态栏嵌入长期多任务进程,必须改为独立的时区解析边界,不得并发改动该变量。
