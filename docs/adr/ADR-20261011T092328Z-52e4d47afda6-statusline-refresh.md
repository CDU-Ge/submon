---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T09:23:28+00:00"
---

# ADR-20261011T092328Z-52e4d47afda6 配额刷新与状态栏渲染分离

## 决定

已采纳。statusline 不启动 watch,配额由现有调度器或显式命令更新。Git 保留同步查询,预算 150ms,缓存 5 秒,非阻塞内核锁防止并发查询,失败尝试先持久化节流。

## 依据与影响

官方状态栏文档 https://code.claude.com/docs/en/statusline 说明高频事件触发与取消语义。原实现缺失缓存时不持久化节流,且启动 watch 后立即释放锁,导致连续启动脱离父进程的刷新任务。修复牺牲自动自愈,换取明确的刷新生命周期。锁文件保留,内核锁随进程终止释放。stdin 读取关闭管道并限制到 1 MiB。

### 会话输入与 Windows 子进程生命周期

statusline 不提供 provider 参数或 badge 开关,provider 仅根据 ANTHROPIC_BASE_URL 识别。安装时移除已取消的参数,保留其他设置。Windows Git 使用 CREATE_NO_WINDOW 和 KILL_ON_JOB_CLOSE Job Object;正常收尾及父进程被终止时关闭 job handle,清理已关联 Git 及后代。无法关联 job 时终止本次 Git 并省略该段。

独立 PowerShell 采样器与循环调用器写入逐条 flush 的 JSONL,以基线、峰值、结束时进程内存和数量观察资源趋势。采样可能漏过短命进程,不把未采到等同于没有创建进程。
