# subscription-monitor 术语

## statusline

Claude Code 高频调用的一次性状态栏命令。读取 stdin 会话数据与配额缓存,输出后退出;不启动后台刷新。Git 查询有缓存、非阻塞锁与时间预算。进程使用 UTC 处理绝对时间戳,高峰提醒显式按 UTC+8 计算。

## watch

一次性配额刷新与催用提醒任务,由 systemd timer、Windows 任务计划程序或用户显式启动。

## 配额缓存

按 provider 和账号标签保存快照、刷新尝试时间、告警状态与历史采样的 JSON 文件。状态栏允许显示过期快照。
