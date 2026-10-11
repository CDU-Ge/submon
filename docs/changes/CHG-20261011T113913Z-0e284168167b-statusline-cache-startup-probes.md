---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T11:39:13+00:00"
---

# CHG-20261011T113913Z-0e284168167b 记录状态栏缓存边界与 Windows 启动对照

## 关联需求

REQ-20261011T092327Z-772c971ad89f E0007。用户询问剩余延迟能否使用缓存解决,要求文件名一致。

## 变更内容

记录现有跨进程数据缓存与不可省略的进程启动边界。独立目录里的实验程序统一名为 submon.exe,测试 console/GUI subsystem、GOMAXPROCS=1 和父进程临时计时器分辨率。生产构建和状态栏运行代码未改动。

## 验证结果

Windows Go 1.26.1 每组 100 次,零间隔、不采样、开启诊断:console 发布构建 P50 32.75ms/P95 35.23ms;单 CPU 32.22ms/33.83ms;GUI subsystem 17.94ms/19.53ms;临时提高父进程计时器分辨率 32.58ms/34.80ms。全部成功,探针 finally 恢复。实测支持启动方式是剩余主要因素,数据缓存无法省去这项成本。GUI 构建仅实验,尚未验证普通 CLI 的控制台连接、shell 等待及交互兼容。原始日志保留远端与本地 statusline-logs/ssh-diagnostics-20261011/cache-question-*。
