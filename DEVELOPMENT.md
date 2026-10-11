# subscription-monitor 开发

Go 版本以 go.mod 为准。构建与回归入口见 Makefile,实现约定见 CLAUDE.md。

- `make build`: 构建当前平台二进制。
- `go test ./cmd ./internal/statusline ./internal/routing -count=1`: CLI、状态栏与路由回归。
- `make test`: 全部单元测试,不请求真实 provider API。
- `make vet`: Go 静态检查。
- `GOOS=windows go build ./...` 与 `GOOS=windows go vet ./...`: Windows 编译及静态检查。Linux 无法执行 Windows 测试二进制。
- 修改的 Go 文件必须通过 gofmt。

statusline 是高频、可取消的一次性任务。配额渲染只读缓存,不得启动后台刷新。Git 查询最多等待 150ms,按目录缓存 5 秒,失败也节流;无法持久化节流时跳过查询。非阻塞内核锁由进程生命周期管理,不得按文件年龄抢占或删除锁文件。stdin 消费后关闭管道,输入最多 1 MiB。

Windows 子进程清理测试为 `TestWindowsGitDiesWithStatuslineParent`,验证父进程被终止后 Job Object 清理 Git。实机循环脚本为 `scripts/statusline-stress.ps1`,用法及日志说明见 `docs/statusline-stress.md`;运行日志保留本地,不提交生成日志。

状态栏性能诊断使用脚本 -TraceStages,必要时加 -NoProcessSampling -DelayMilliseconds 0。单次 duration_ms 排除退出后内存查询与日志写入;总循环、准备和尾部采样单独报告。程序内 SUBMON_STATUSLINE_TIMING=1 只写 stderr,默认不开启。

Cobra MousetrapHelpText 必须保持空字符串,避免 Windows 每次 Execute 创建系统进程快照来检测 Explorer 启动。cli_ms 从 cmd.Execute 入口计到 RunE 入口,覆盖 Cobra 前置检查;outside_cli_ms 记录其余启动及收尾开销。

跨进程边界诊断 before_cli_ms/after_run_ms 使用共享系统 UTC 时钟,仅在 -TraceStages 且二进制提供时间戳时计算;默认路径不读取额外时钟。记录保留精度与校时误差,不得将其中一个阶段直接归因于特定系统服务。

配置解析与生成使用 github.com/pelletier/go-toml/v2(TOML 1.1)。不得引入在包初始化中读取系统本地时区的依赖:Windows 高频短进程会重复付出这项开销。statusline 在共享 Run 入口、读取数据及启动 goroutine 之前设定 time.Local = time.UTC;时间戳按绝对时刻计算,高峰提醒显式使用 UTC+8,旧带 offset 的缓存兼容,新状态栏缓存写 UTC。其他命令保持系统时区。

Windows 原生测试须区别 FileMode 与 ACL:POSIX 0600 断言仅适用于 Unix。Git 超时集成测试预热复制后的测试可执行文件,避免首次冷加载在进入模拟 Git 之前耗尽预算;生产 Git 预算保持 150ms。

Windows 状态栏入口为 `apps/statusline`,仅依赖 `internal/statusline` 共享渲染与资源管理。URL 识别规则归 `internal/routing`,HTTP 适配器归 `internal/provider`;轻量程序不得链接 Cobra、net/http、调度或登录初始化。`internal/display` 保存面板与状态栏共用的数字、时间及颜色阈值规则。

Windows 构建使用 `scripts/build.ps1` 或 `make build`;交叉发布使用 `make release`。先构建 GUI subsystem 的 `internal/statuslinebundle/assets/submon.exe`,再嵌入普通 console CLI。产物不提交。普通干净 `go build` 保留兼容回退;它不代表包含轻量组件的发布构建。安装器将组件部署到 appdir.Data()/statusline/submon.exe,相同内容不写入,替换失败不更新设置。预览不部署,自定义命令正常安装时保留。

Windows 实际安装回归: `powershell -NoProfile -ExecutionPolicy Bypass -File scripts/tests/statusline-standalone.tests.ps1 -Submon ./submon.exe`。脚本隔离用户配置并保留日志,检查部署、旧命令迁移、重复安装、三种布局/颜色输出一致与自定义命令保留。普通 CLI 保持 console subsystem,轻量组件只用于管道调用。
