# Windows 状态栏资源测试

在存在 Git 分支和已跟踪文件的仓库运行,使用当前会话的环境变量。脚本不写凭据、不修改配额缓存或杀死既有进程。未识别 ANTHROPIC_BASE_URL 时省略配额;要测试缺失配额路径,可在测试用 PowerShell 会话设置实际 provider 的 base URL。

```powershell
go build -o submon.exe .
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Workspace . -Iterations 10000
```

默认最多 10 分钟、10000 次调用,间隔 10ms。每次启动新进程并向 stdin 写入 UTF-8 mock JSON,重定向 stdout/stderr;等价于循环管道输入,但直接使用 ProcessStartInfo,可限制超时和禁止测试本身创建控制台窗口。默认 mock 带稳定 session_id、工作区和上下文字段;可用 `-MockDataFile payload.json` 替换。

```powershell
# 更密集的循环,最多 30 分钟
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Iterations 0 -DurationSeconds 1800 -DelayMilliseconds 0
# 指定日志位置与采样频率
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -SampleMilliseconds 20 -LogDirectory .\statusline-logs\after
```

日志每条写完立即 flush,结束后继续采样 3 秒。Ctrl-C 正常触发 finally 清理与写总结;若进程或设备被强制结束,总结可能缺失,此前 JSONL 仍可读取,最后一行可能不完整。日志不自动删除,默认目录名包含时间和随机后缀。Git 压测日志保留本地,已在 Git 中忽略默认输出目录。

| 文件 | 内容 |
| --- | --- |
| metadata.json | 二进制路径与 SHA256、工作区、采样参数、主 PowerShell PID、路由 URL |
| mock-data.json | 实际输入 |
| invocations.jsonl | 每次调用的 PID、端到端耗时、启动/input/等待退出/输出收尾/内存查询耗时、退出码、stdout/stderr;可选程序内计时 |
| process-samples.jsonl | 独立采样的 PID、名称、启动时间、private bytes、working set、句柄和 CPU 时间;各进程组总量 |
| process-summary.json | 开启采样时记录基线、峰值、结束时各组数量与内存 |
| run-summary.json | 完成次数、失败次数、停止原因;准备/循环/收尾时间,单次 P50/P95 与程序内各阶段均值 |
| errors.log / observer-errors.log | 测试或采样异常,仅出现异常时创建 |

默认单次调用 3 秒超时。总体 private bytes 比基线增长超过 512 MiB,或进程数增加超过 64,脚本停止启动下一次调用并保存日志。可用 `-MaxGrowthMB` / `-MaxExtraProcesses` 调整。仅超时的本次子进程会被终止;不清理系统上其他 Git/OpenConsole 进程。旧版本若留下脱离父进程的 watch,停止循环并不保证其退出,比较前后版本时应先完成旧版本清理。

观察方法:

1. 先运行一个短测试确认退出码正常,再扩大到 10000 次。
2. 比较各组 baseline、peak、final;重点查看 Git、OpenConsole、conhost 数量以及同一 PID 的 private bytes 和句柄是否持续增长。每次 statusline 的 PID 会改变,不能把不同 PID 的内存视为同一个进程泄漏。
3. 同一 cwd 的 Git 查询按 5 秒节流;次数不应随 statusline 调用次数等比例增长。缓存命中时允许看不到 Git。
4. 采样统计包含已有同名进程,可能受其他应用影响。寿命短于采样间隔的进程可能漏采;“未采到”不能证明没启动。工作集含共享页,总量不能直接当作独占物理内存;优先结合 private bytes、系统任务管理器与结束时是否回到基线判断。
5. 此循环串行调用,检验累计资源变化。另运行以下 Windows Go 回归验证高频 Git 超时节流、父进程终止后子进程清理、内核锁释放及 stdin 清理:

```powershell
go test ./cmd -run 'TestGitTimeoutAndThrottle|TestWindowsGitDiesWithStatuslineParent|TestStatuslineLock|TestStdin|TestExpiredQuota|TestStatuslineHasNoProviderFlags' -count=1 -v 2>&1 | Tee-Object -FilePath .\statusline-logs\go-regression.log
```

Windows Git 使用 CREATE_NO_WINDOW,并绑定 KILL_ON_JOB_CLOSE Job Object。[微软 Job Object 文档](https://learn.microsoft.com/en-us/windows/win32/procthread/job-objects)说明该标志会在最后一个 job handle 关闭时终止关联进程。循环测试日志用于实机验证,不预先断言内存泄漏已排除。

## 调用性能计时

默认每次有 10ms 间隔,结束还有 3 秒尾部采样。总耗时除以次数包含这些时间,不能用来判断单次 statusline 延迟。schema 2 的 duration_ms 从启动子进程之前开始,到进程退出且输出排空结束;退出后的内存查询和日志写入另行统计。

```powershell
# 先隔离采样、等待间隔和收尾开销,分解程序内耗时
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Iterations 200 -DelayMilliseconds 0 -NoProcessSampling -TraceStages
# 再启用进程采样比较,保持其他参数相同
.\scripts\statusline-stress.ps1 -Submon .\submon.exe -Iterations 200 -DelayMilliseconds 0 -TraceStages
```

-NoProcessSampling 跳过进程采样、3 秒收尾和进程增长保护,仍保留单次超时及完整调用日志。默认不查询已退出进程的内存属性,独立采样仍记录内存;需要这些属性时使用 -CaptureInvocationMemory,开销记录为 metrics_ms。相关峰值字段默认 null。

-TraceStages 在子进程中启用 SUBMON_STATUSLINE_TIMING=1,只向 stderr 写一行 JSON。程序内计时拆分配置加载、stdin 读取与解析、渲染,run_ms 包含 RunE 内总耗时。outside_run_ms 是端到端耗时减去 run_ms,包含系统创建进程、Go 初始化、CLI 入口及输出收尾,不能将其全部视作某一种原因。

run-summary.json 的 latency_ms 给出单次调用均值、P50/P95、最大值。均值覆盖全部调用;分位数和最大值使用最近最多 10000 次的固定容量样本,避免连续运行时统计器无限增长。loop_seconds 包含进程调用、日志、内存查询与实际等待间隔;observation_tail_seconds 独立显示最后采样的耗时。application_stage_mean_ms 在诊断开启时给出程序内各阶段与 RunE 之外的平均耗时。

脚本优先按字面文件路径解析 -Submon,默认路径中的 .. 会规范化;通过 PATH 查找时仅选择第一个应用,避免多个候选路径被拼接。路径回归可运行 `pwsh -NoProfile -File scripts/tests/statusline-stress-path.tests.ps1`,也可使用 Windows PowerShell。

## Windows CLI 入口检查

RunE 之外很慢时,须区分 CLI 执行前检查和系统启动/Go 初始化。本程序关闭 Cobra 默认 Explorer 启动检测:该检测在 Windows 会获取系统进程快照,对每次 statusline 调用都执行,与渲染内容无关。关闭后双击可执行文件不再展示 Cobra 的暂停提示;通过终端使用命令。

重新编译 Go 二进制后,-TraceStages 还会记录 cli_ms:从 cmd.Execute 入口到 statusline RunE 入口的耗时。摘要中 outside_cli_ms 是端到端耗时减去 run_ms 和 cli_ms,仍包含系统启动、Go 初始化及输出收尾,不能直接认定某个系统服务为原因。仅更新脚本、未重新构建二进制时不会出现这项新计时。


重新构建后,诊断行提供 cli_started_unix_ms 与 run_finished_unix_ms。脚本使用同一系统的 UTC 时钟与父进程 Stopwatch,把剩余耗时拆成 before_cli_ms(启动调用前到 cmd.Execute 入口)和 after_run_ms(RunE 计时结束到父进程观察到退出)。前者包含进程创建、加载、Go runtime 与包初始化;后者包含诊断写入、CLI 返回、进程退出与父进程唤醒。这是定位边界,不能单独指认某个系统组件。两项逐次写入 invocations.jsonl,均值写入 application_stage_mean_ms 并打印。时钟精度或系统校时可能导致小误差,负值原样保留;旧二进制缺少时间戳时保持 null,不计算这两项均值。

2026-10-11 Windows 实测:process_start_ms 平均 3.69ms,wait_exit_ms 82.59ms,drain_output_ms 0.03ms;禁用 Explorer 检测后整体 P50 仍约 85ms,主因尚未定位。Linux 同脚本 200 次(仅移除平台限制,不采样、零间隔、开启诊断)P50 3.10ms/P95 3.69ms,进入 CLI 前平均 2.61ms、完成至观察退出 0.22ms。该对照不能替代 Windows 实测,日志保留于 statusline-logs/local-boundaries-20261011T104419Z/。


### Windows SSH 实机结果(2026-10-11)

在 administrator@192.168.49.11 上,当前源码放入 E:\Projects\submon\statusline-logs\ssh-diagnostics-20261011\source,测试未覆盖远端原源码或 submon.exe。所有实机日志位于该日志根目录,本地同路径 statusline-logs/ssh-diagnostics-20261011/ 保留副本。

| 程序与条件 | 调用次数 | P50 | P95 |
| --- | ---: | ---: | ---: |
| 修复前 Go 1.26.1 本机构建 | 100 | 84.02ms | 92.02ms |
| 最小 Go 1.26.1 程序 | 100 | 26.52ms | 28.54ms |
| 修复后 Go 1.26.1 本机构建 | 200 | 32.45ms | 34.94ms |
| 修复后 Go 1.27 交叉构建 | 200 | 30.47ms | 32.94ms |
| 修复后 Go 1.26.1,开启进程采样 | 500 | 32.58ms | 34.87ms |

未注明采样的对照均为零间隔、-NoProcessSampling -TraceStages。inittrace-go126 的 stderr 显示 TOML 库初始化约 49ms,来源为 eager 读取本地时区。只更换 TOML 库会将该开销移到旧 JSON 时间戳解析;statusline 同时使用 UTC 后才消除重复系统时区初始化。新配置编解码器支持 TOML 1.1,配置兼容回归覆盖中文、Windows literal 路径、多行凭据与设置往返;旧带 offset 的 JSON 缓存仍保持原时刻。诊断仍默认关闭。

sampled-500 共 175 次采样。结束时测试程序/Git 均为 0 个,OpenConsole 数量为基线的 3 个,conhost 为基线的 6 个;追踪组 private bytes 从约 385.71MiB 到 383.55MiB。采样包含其他用户进程,本次短时实验未见残留累积,不能据此证明长期无泄漏。


### 缓存与剩余启动成本对照

现有配额 JSON、按目录 Git 5 秒缓存、按会话上下文缓存跨进程保留。修复时区后,配置/输入/渲染合计约 1–2ms;这些阶段增加输出缓存不会消除进入 CLI 前的成本。完整输出依赖当前 stdin、环境与倒计时,复用旧整行还需要处理失效与更新。

同台 Windows,同 Go 1.26.1 源码、零间隔、不采样、TraceStages,每组 100 次:

| 构建或诊断条件 | P50 | P95 |
| --- | ---: | ---: |
| 普通 console 发布构建(-trimpath -s -w) | 32.75ms | 35.23ms |
| 同构建,GOMAXPROCS=1 | 32.22ms | 33.83ms |
| 同源码和编译器,GUI subsystem 构建 | 17.94ms | 19.53ms |
| console 构建,仅测量父进程临时 timeBeginPeriod(1) | 32.58ms | 34.80ms |

GUI subsystem 是定位实验,未更改生产构建或替换项目程序。后续采用之前须验证普通 CLI 的控制台连接、shell 等待与交互输入语义。timeBeginPeriod 探针在 finally 中恢复,未更改系统设置。实验程序均名为 submon.exe,目录区分构建。日志保留于远端和本地 statusline-logs/ssh-diagnostics-20261011/cache-question-*。
