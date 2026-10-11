# subscription-monitor 术语

## statusline

Claude Code 高频调用的一次性状态栏命令。读取 stdin 会话数据与配额缓存,输出后退出;不启动后台刷新。Git 查询有缓存、非阻塞锁与时间预算。进程使用 UTC 处理绝对时间戳,高峰提醒显式按 UTC+8 计算。

## Windows 状态栏组件

由 `submon install` 从主 CLI 内嵌资源部署的独立一次性程序,路径为 `%LocalAppData%\submon\statusline\submon.exe`。共享状态栏实现与缓存,使用 GUI subsystem 避免启动控制台;没有常驻服务。主 CLI 与组件都名为 submon.exe。

## watch

一次性配额刷新与催用提醒任务,由 systemd timer、Windows 任务计划程序或用户显式启动。

## 配额缓存

按 provider 和账号标签保存快照、刷新尝试时间、告警状态与历史采样的 JSON 文件。状态栏允许显示过期快照。
