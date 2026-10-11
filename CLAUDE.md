# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目

`submon` — Go 订阅配额监控 CLI(Windows 内嵌独立状态栏组件)(智谱 bigmodel.cn/z.ai + MiniMax + OpenCode zen),服务三个场景:人查(`status`)、Claude Code 状态栏(`statusline`,高频调用、只读缓存)、定时催用提醒(`watch` + 定时任务(systemd timer / 任务计划程序)+ Gotify)。UI 文案面向简体中文用户,所有用户可见字符串用中文。

## 常用命令

```sh
make build          # 编译到 ./submon(注入 git version)
make install        # 安装到 ~/.local/bin/submon
make test           # 全部测试
go test ./internal/predict/ -run TestWeeklyFate -v   # 单个测试
make vet / make fmt # 静态检查 / gofmt + go mod tidy
make release        # 交叉编译 linux/darwin/windows 到 dist/
GOOS=windows go build ./... && GOOS=windows go vet ./...   # Windows 回归(本机跑不了 windows 二进制,至少守住编译 + vet)
```

无独立 lint 工具;`go vet` 是唯一静态检查,gofmt 必须零输出(`make fmt`)。

## 架构

数据流(理解这个就理解了整个程序):

```
provider.Fetch()  ──►  model.Snapshot(统一数据模型)──►  cache(~/.cache/submon/cache.json)
   ▲                                                              │
   │ refresh.One() 是唯一写入口                                    ├─► statusline(只读,高频)
   │                                                              ├─► status(TTL 内读缓存)
watch / status --force                                            └─► watch 评估告警状态机
```

- `internal/model`:**Window/Snapshot 归一化模型**。所有 provider 解析成同一形状。关键语义:Zhipu/OpenCode 的 `percentage`/`percent` 是*已用%*,MiniMax 的 `*_remaining_percent` 是*剩余%*——归一层统一翻转为"剩余"。`Window.PredictedWastePct()` 用速率外推 5h 窗口重置时的浪费。`AllProviders()` 是 provider 清单的唯一权威来源。
- `internal/provider`:Zhipu 按配额条目的 `(type, unit, number)` 元组识别窗口(TOKENS_LIMIT unit=3/5 → 5h,unit=6/1 → 周,TIME_LIMIT unit=5/1 → 月度 MCP);MiniMax 响应字段在顶层(无 data 包装)、时间戳是 epoch 毫秒;OpenCode zen `/zen/go/v1/usage` 的 `percent` 是已用%、`resetsAt` 是 RFC3339,rolling/weekly/monthly 三窗口。`registry.Build` 解析凭据,顺序:**环境变量 > 默认账号 > 内置默认**(zhipu 默认 base_url `https://open.bigmodel.cn/api/anthropic`)。**新增 provider 时**:provider 实现 + `registry.BuildOne/BuildAll` 分支 + `DetectFromBaseURL` + `loginRegistry` 登记 + `AllProviders()` 注册,cmd 层一律遍历 `model.AllProviders()`,不要硬编码列表。
- `internal/cache`:单 JSON 文件,按 `(provider, label)` 编码 key — 默认 Key 走裸 provider 名(`"zhipu"`)与历史兼容,命名 Key 走 `"zhipu/alice"`。`File.GetLabeled` / `SetLabeled` 是多 key 入口,`Get` / `Set` 仍走默认 key(单 key 老路径)。原子写(temp+rename+flock)。存 snapshot(含 `KeyLabel`)、`RefreshedAt`(刷新尝试时间,保留旧缓存兼容)、`AlertState`(告警状态机记忆)、`History`(转换率采样,每 Key 各自一份)。
- **账号模型**:凭据统一存 `[[accounts]]`(`config.Account{Provider, Label, Token, BaseURL}`),gotify 也是账号(`BaseURL`=服务地址、`Token`=app_token)。`config.ProviderKeys(p)` 按 provider 过滤账号并规范化:空 label 的第一个账号即 `default`,其余自动编号 `key-N`,重复 label 后写覆盖前写,空 token 跳过。`registry.BuildAll(p, cfg)` 一次性构造所有账号的 Source,每个 `LabeledSource.Fetch` 把 `Label` 写入 `Snapshot.KeyLabel`。`refresh.Many` 按账号独立取数 + 独立写 cache;`watch` 每个账号各自跑告警状态机(`AlertState` 也按 label 隔离)。status / statusline 多账号段并列渲染,default 与单账号格式**完全一致**(不显示 "default" 字符串)。旧格式(`[zhipu] token` + `[[zhipu.keys]]` + `[notify.gotify] url/app_token`)由 `config.Load()` 内存迁移(见 `legacyConfig`),任何一次 Save 即落盘新 schema。
- **账号配置入口**:`submon login`(默认添加:交互选 provider/标签/字段);`submon login ls` 列账号;`submon login remove [provider [label]]` 删除(无参数交互选,非 TTY 需 `-f`)。`login ls` 永不打印明文。
- `internal/predict`:**weekly 必然浪费**检测——人每天最多 3 个 5h 窗口(要休息),`转换率(中位数 Δweekly/Δ5h) × 剩余窗口数` 与 weekly 剩余对比,算出"无论如何都用不完"的部分。无历史时用保守假设 5h/168h≈3%/窗口。
- `internal/secret`:AES-256-GCM,密钥**自管理**存 `~/.config/submon/secret.key`(0600),不依赖系统 keyring。config.toml 里 token 存 `enc:v1:nonce:ct`。
- `cmd/watch.go`:两级告警状态机(weekly 必然浪费随时评估;5h 窗口仅重置前 `remind_before` 内评估),组合状态 `weekly:short` 变化才推送,推送失败不推进状态。语义是**催用**(要到期了抓紧用),不是不足告警。
- `cmd/statusline.go` 是 Cobra 适配器,`internal/statusline` 共享实现,`apps/statusline` 是独立入口:必须秒回、永不联网、永不 hard-fail。渲染不得启动后台 `watch`;配额由定时任务或显式 `watch` 更新。Git 查询按目录缓存 5 秒,150ms 超时,失败同样节流,操作系统非阻塞锁随进程退出释放。provider 识别:仅 `ANTHROPIC_BASE_URL` 自动识别(走 `internal/routing` 中各 provider 文件声明的 `xxxRules()`,`DefaultDetectionRules` 聚合;glob `*.z.ai` / path 前缀 / priority 在 `internal/routing/routing.go:Rule`)。未命中 statusline 不显示配额段(没有 default_provider 兜底,避免误导)。statusline 不展示 provider badge,不提供 provider 参数或 badge 开关。**检测规则不进 config.toml** — 加 provider 时改文件。
- 凭据解密入口统一是 `cmd/root.go` 的 `loadDecryptedConfig()`(config.Load + DecryptSecrets);`login ls` / `check` 展示存储形态的场景用原始 `config.Load()`。

## 凭据配置(新增)

**统一入口**:`submon login` — 配 zhipu/minimax/opencode/kimi/gotify 全部走同一路径。**不要**用 `submon login <token>` 这种命令行参数形式(token 会进 shell history)。**不要**复活已删除的 `submon token` / `submon auth`。

- `submon login` — 走 `cmd/login.go` 的 `loginRegistry`(`loginField{Secret, Required}`),依次询问 provider / 标签 / token(TTY 时无回显,管道时逐行读);落盘前 AES-256-GCM 加密
- `submon login ls` — 列所有账号与存储形态(`enc` / `PLAINTEXT` / `-`),永不打印明文
- `submon login remove [provider [label]]` — 删除账号(无参数交互选择;非 TTY 需 `-f`)
- `submon login discover` — 扫 env / CC settings 询问式导入

注册表是 `cmd/login.go` 的 `loginRegistry`,新 provider **必须** 在这里登记,不允许在 cmd 层写 `if name == "xxx"` 分支。`Discover` 的 setter 统一走 `accountSetter(provider, field)`。

Gotify 走同一安全等级:URL 明文、`app_token` 加密,与 provider 同流程、同 `[[accounts]]` 存储。

测试规范:统一在 `cmd/login_test.go` 用 `loginTestProviders` 表驱动,每个 provider 跑 add/ls/remove 三条用例,新增 provider 必须扩同一张表;`TestLoginRegistryCoversAllProviders` 与 `TestLoginRejectsUnknownProvider` 守住"注册表 = 唯一真源"约束。

## 平台支持(Linux / macOS / Windows)

平台差异一律走构建标签文件(`xxx_unix.go` / `xxx_windows.go`),命令层不写 `if runtime.GOOS`:

- **路径**:`internal/appdir` 是唯一真源 —— POSIX 走 XDG(`~/.config`、`~/.cache`、`~/.local/share`,macOS 也沿用以免迁移丢配置),Windows 走 `%AppData%\submon`(配置/密钥)与 `%LocalAppData%\submon`(缓存/补全)。config / secret / cmd 全部从这里取;**不要再手写 `os.Getenv("XDG_*")`**。
- **定时任务**:`cmd/schedule.go` 定义 `scheduler` 接口,`schedule_systemd.go`(systemd 用户 timer)与 `schedule_windows.go`(任务计划程序 + UTF-16LE 的 VBS 启动器,避免每 N 分钟闪黑窗)各实现一份。install / check / notify status 只消费接口,新增平台 = 新增一个实现文件。
- **控制台**:Windows 需要 UTF-8 代码页 + `ENABLE_VIRTUAL_TERMINAL_PROCESSING`(`cmd/console_windows.go`,`prepareConsole` 在 rootCmd 的 PersistentPreRun 里调),终端不能 ANSI 时 `consoleSupportsANSI()` 让 `useColor()` 自动关色。
- **信号**:`cmd/signal_*.go` 的 `termSignals()`(Windows 只有 Ctrl-C,没有 SIGTERM)。
- **渲染子进程**:`internal/statusline/procattr_*.go` 的 `hideWindowProcAttr()`隐藏 Windows Git 控制台;statusline 不启动脱离父进程的刷新任务。Windows Git 加入 KILL_ON_JOB_CLOSE Job Object,父进程被取消时清理 Git 及其后代。`internal/statusline/statusline_lock_*.go` 使用非阻塞 flock / LockFileEx,锁文件不删除。
- **缓存锁 / 原子替换**:`internal/cache/lock_unix.go`(flock)与 `lock_windows.go`(LockFileEx);`rename_*.go` 是替换实现 —— Windows 上并发读会挡住 MoveFileEx(Go 打开文件不带 FILE_SHARE_DELETE),需要退避重试。
- PowerShell 调用统一走 `cmd/powershell.go`(强制 UTF-8 输出,解析属性名而不是本地化的文本)。

## 约定

- 用户可见文案里的路径不许写死 `~/.config/submon`(Windows 上指错地方),用 `cmd/pathhint.go` 的 `configPathHint()` / `cachePathHint()` / `secretKeyHint()`。
- 状态栏/终端输出:**禁用 dim 灰色**(可读性),辅助信息用 `ansiWhite`(默认前景色 `\033[0;39m`),彩色只留给语义信息(百分比阈值变色绿/黄/红)。
- statusline 排版有 a/b/c 三套(`statusline.layout` 配置,`SUBMON_LAYOUT` 环境变量可临时切换预览),默认 a;改格式时三套都要对齐。
- 新增 config 字段要同时改:`Config` 结构体 toml tag、`Default()`、`Load()` 的兜底逻辑,以及 `submon config` 的合并迁移逻辑(保持"不丢现有配置"承诺)。
- 通知推送文案带预测依据(数学过程写进正文),标题首词标紧急度(【紧急】/【提醒】)。
- 测试不打真实 API;provider 测试用 `httptest.NewServer` mock 原始响应(payload 形状来自真实抓包,见各 provider 测试里的注释)。

## 遗留物

`scripts/zhipu-usage-query.mjs`(原型脚本,有 bug:所有 TOKENS_LIMIT 都标成 5h)、`node_modules`/`package.json`(早期 bun 依赖)——Go 实现的参考物,与构建无关。

Windows 发布先构建 `apps/statusline` 为 GUI subsystem submon.exe,再经 `internal/statuslinebundle` 嵌入 console 主 CLI;使用 `scripts/build.ps1` / `make build` / `make release`。install 部署到 `%LocalAppData%\submon\statusline\submon.exe`,正常安装保留自定义状态栏,显式 install claude 才替换。轻量入口不依赖 Cobra 或 HTTP 适配器,共用实现避免输出分叉。干净普通 go build 无组件时回退主 CLI。
