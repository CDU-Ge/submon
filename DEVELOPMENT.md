# subscription-monitor 开发

Go 版本以 go.mod 为准。构建与回归入口见 Makefile,实现约定见 CLAUDE.md。

- `make build`: 构建当前平台二进制。
- `go test ./cmd -count=1`: CLI 与状态栏回归。
- `make test`: 全部单元测试,不请求真实 provider API。
- `make vet`: Go 静态检查。
- `GOOS=windows go build ./...` 与 `GOOS=windows go vet ./...`: Windows 编译及静态检查。Linux 无法执行 Windows 测试二进制。
- 修改的 Go 文件必须通过 gofmt。

statusline 是高频、可取消的一次性任务。配额渲染只读缓存,不得启动后台刷新。Git 查询最多等待 150ms,按目录缓存 5 秒,失败也节流;无法持久化节流时跳过查询。非阻塞内核锁由进程生命周期管理,不得按文件年龄抢占或删除锁文件。stdin 消费后关闭管道,输入最多 1 MiB。

Windows 子进程清理测试为 `TestWindowsGitDiesWithStatuslineParent`,验证父进程被终止后 Job Object 清理 Git。实机循环脚本为 `scripts/statusline-stress.ps1`,用法及日志说明见 `docs/statusline-stress.md`;运行日志保留本地,不提交生成日志。
