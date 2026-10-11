---
actor_id: "2bb2de2a5e11edce"
recorded_at: "2026-10-11T12:08:28+00:00"
---

# ADR-20261011T120828Z-2acb91bea879 Windows 独立一次性状态栏组件

## 决定

采用 apps/statusline 小入口与 internal/statusline 共享实现。Windows helper 使用 GUI subsystem,仍名 submon.exe;主 CLI 保持 console subsystem,内嵌组件经 install 部署到用户数据目录。纯 URL 路由提取到 internal/routing,数值与颜色阈值格式提取到 internal/display。没有常驻进程。

## 依据与影响

同机 GUI 实验约 18ms,完整 console CLI 约 33ms,状态栏逻辑约 1–2ms。数据缓存无法消除进程启动成本,改变全部 CLI subsystem 会影响控制台和交互。独立组件复用全部渲染与资源约束,不链接 Cobra/HTTP 适配器。Windows 构建先生成 helper 再嵌入 CLI;普通干净 go build 无资源时兼容回退。预览不部署,相同内容不写入,替换失败不更新设置;迁移当前 CLI 命令与旧参数,正常 install 保留其他命令,显式 install claude 可接管。代价为发布包增加约 3.3MB,仍需每次启动进程。Windows 原生安装集成、输出比较、Git Bash 与资源压力测试验证,详见 docs/statusline-stress.md。
