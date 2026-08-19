# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## 项目

`submon` — Go 单二进制订阅配额监控(智谱 bigmodel.cn/z.ai + MiniMax + OpenCode zen),服务三个场景:人查(`status`)、Claude Code 状态栏(`statusline`,高频调用、只读缓存)、定时催用提醒(`watch` + systemd timer + Gotify)。UI 文案面向简体中文用户,所有用户可见字符串用中文。

## 常用命令

```sh
make build          # 编译到 ./submon(注入 git version)
make install        # 安装到 ~/.local/bin/submon
make test           # 全部测试
go test ./internal/predict/ -run TestWeeklyFate -v   # 单个测试
make vet / make fmt # 静态检查 / gofmt + go mod tidy
make release        # 交叉编译 linux/darwin 到 dist/
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
- `internal/provider`:Zhipu 按配额条目的 `(type, unit, number)` 元组识别窗口(TOKENS_LIMIT unit=3/5 → 5h,unit=6/1 → 周,TIME_LIMIT unit=5/1 → 月度 MCP);MiniMax 响应字段在顶层(无 data 包装)、时间戳是 epoch 毫秒;OpenCode zen `/zen/go/v1/usage` 的 `percent` 是已用%、`resetsAt` 是 RFC3339,rolling/weekly/monthly 三窗口。`registry.Build` 解析凭据,顺序:**环境变量 > 配置文件 > 内置默认**(zhipu 默认 base_url `https://open.bigmodel.cn/api/anthropic`)。**新增 provider 时**:provider 实现 + `registry.Build` 分支 + `DetectFromBaseURL` + config 结构体 + `AllProviders()` 注册 + token 槽位 + `generate config` 迁移,cmd 层一律遍历 `model.AllProviders()`,不要硬编码列表。
- `internal/cache`:单 JSON 文件,按 provider 分 key。原子写(temp+rename+flock)。存 snapshot、`RefreshedAt`(statusline 自愈节流)、`AlertState`(告警状态机记忆)、`History`(转换率采样)。
- `internal/predict`:**weekly 必然浪费**检测——人每天最多 3 个 5h 窗口(要休息),`转换率(中位数 Δweekly/Δ5h) × 剩余窗口数` 与 weekly 剩余对比,算出"无论如何都用不完"的部分。无历史时用保守假设 5h/168h≈3%/窗口。
- `internal/secret`:AES-256-GCM,密钥**自管理**存 `~/.config/submon/secret.key`(0600),不依赖系统 keyring。config.toml 里 token 存 `enc:v1:nonce:ct`。
- `cmd/watch.go`:两级告警状态机(weekly 必然浪费随时评估;5h 窗口仅重置前 `remind_before` 内评估),组合状态 `weekly:short` 变化才推送,推送失败不推进状态。语义是**催用**(要到期了抓紧用),不是不足告警。
- `cmd/statusline.go`:必须秒回、永不联网、永不 hard-fail。缓存过期时 fork 后台 `watch --quiet` 自愈(下帧生效)。provider 识别:`--provider` > `ANTHROPIC_BASE_URL` 域名 > 配置默认。模型名含 GLM/zhipu/minimax 时省略对应 provider badge(信息冗余)。
- 凭据解密入口统一是 `cmd/token.go` 的 `loadDecryptedConfig()`(config.Load + DecryptSecrets);generate/ls 等展示存储形态的场景用原始 `config.Load()`。

## 凭据配置(新增)

**统一入口**:`submon auth <provider>` — 配 zhipu/minimax/opencode/kimi/gotify 全部走同一路径。**不要**用 `submon auth <provider> <token>` 这种命令行参数形式(token 会进 shell history)。**不要**用 `submon token set <slot> <value>`(同上,且 token 进了 `~/.bash_history` 之后即使再加密也救不回)。

- `submon auth <provider>` — 走 `<provider>` 注册表,从 stdin 读 token(TTY 时无回显,管道时明文);落盘前 AES-256-GCM 加密
- `submon auth ls` — 列每个 provider 存储形态(`enc` / `PLAINTEXT` / `-`),永不打印明文
- `submon auth remove <provider>` — 清空该 provider 的凭据
- `submon auth check` — 解密+完整性体检,适合 systemd watchdog

注册表是 `cmd/auth.go` 的 `authRegistry`(`authField{Secret, Required}` + `Apply`),新 provider **必须** 在这里登记,不允许在 cmd 层写 `if name == "xxx"` 分支。`cmd/token.go` 保留用于向后兼容,但已 deprecated。

Gotify 走同一安全等级:URL 明文、`app_token` 加密,与 provider 同流程。

测试规范:统一在 `cmd/auth_test.go` 用 `authTestProviders` 表驱动,每个 provider 跑 set/ls/remove/check 四条用例,新增 provider 必须扩同一张表;`TestAuthRegistryCoversAllProviders` 与 `TestAuthRejectsUnknownProvider` 守住"注册表 = 唯一真源"约束。

## 约定

- 状态栏/终端输出:**禁用 dim 灰色**(可读性),辅助信息用 `ansiWhite`(默认前景色 `\033[0;39m`),彩色只留给语义信息(百分比阈值变色绿/黄/红)。
- statusline 排版有 a/b/c 三套(`statusline.layout` 配置,`SUBMON_LAYOUT` 环境变量可临时切换预览),默认 a;改格式时三套都要对齐。
- 新增 config 字段要同时改:`Config` 结构体 toml tag、`Default()`、`Load()` 的兜底逻辑,以及 `submon generate config` 的合并迁移逻辑(保持"不丢现有配置"承诺)。
- 通知推送文案带预测依据(数学过程写进正文),标题首词标紧急度(【紧急】/【提醒】)。
- 测试不打真实 API;provider 测试用 `httptest.NewServer` mock 原始响应(payload 形状来自真实抓包,见各 provider 测试里的注释)。

## 遗留物

`scripts/zhipu-usage-query.mjs`(原型脚本,有 bug:所有 TOKENS_LIMIT 都标成 5h)、`node_modules`/`package.json`(早期 bun 依赖)——Go 实现的参考物,与构建无关。
