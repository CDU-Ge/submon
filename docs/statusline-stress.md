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
| invocations.jsonl | 每次调用的 PID、耗时、退出码、超时、峰值工作集/分页内存、stdout/stderr |
| process-samples.jsonl | 独立采样的 PID、名称、启动时间、private bytes、working set、句柄和 CPU 时间;各进程组总量 |
| process-summary.json | 基线、峰值、结束时各组数量与内存 |
| run-summary.json | 完成次数、失败次数、停止原因 |
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
