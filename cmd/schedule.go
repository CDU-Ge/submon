package cmd

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// submon 的"定时刷新"在 POSIX 上是 systemd 用户 timer,在 Windows 上是任务
// 计划程序里的一条任务。命令层(install / check / notify status)只依赖下面
// 这套接口,平台实现各自放在 schedule_systemd.go / schedule_windows.go —
// 加一个平台只需要加一个实现文件,不需要在命令里写 if runtime.GOOS。
type scheduler interface {
	// Name 是给用户看的机制名,如 "systemd 用户 timer"。
	Name() string
	// Install 注册并启用定时任务;interval 是 --interval 的原始字符串,
	// 平台自己解析(格式要求不同)。
	Install(cmd *cobra.Command, bin, interval string) error
	// Uninstall 停止并删除定时任务;未注册时视为成功。
	Uninstall(cmd *cobra.Command) error
	// Print 预览 Install 会写入什么,不落盘。
	Print(cmd *cobra.Command, bin, interval string) error
	// Status 查询当前注册状态,供 check / notify status 展示。
	Status() scheduleStatus
}

// scheduleStatus 是定时任务的可观察状态,由平台实现填充。
type scheduleStatus struct {
	// Ready: 定时任务存在且已启用。
	Ready bool
	// Missing: 平台调度器本身不可用(没有 systemctl / schtasks 命令)。
	Missing bool
	// NextRun: 下次触发时间的人类可读字符串,空 = 未知。
	NextRun string
	// Note: 补充说明(原始状态串 / 未就绪原因)。
	Note string
}

// intervalMinutes 把 --interval 解析成整分钟,Windows 任务计划程序只支持
// 分钟粒度(1-1439)。systemd 那边直接用原始字符串,不走这里。
func intervalMinutes(interval string) (int, error) {
	d, err := time.ParseDuration(strings.TrimSpace(interval))
	if err != nil {
		return 0, fmt.Errorf("间隔 %q 无法解析 — 用 Go 时长格式,如 5m、15m、1h", interval)
	}
	if d < time.Minute {
		return 0, fmt.Errorf("间隔 %s 太短 — Windows 任务计划程序最小 1m", d)
	}
	m := int(d / time.Minute)
	if m > 1439 {
		return 0, fmt.Errorf("间隔 %s 太长 — Windows 任务计划程序最大 1439m(约 24h)", d)
	}
	if d%time.Minute != 0 {
		return 0, fmt.Errorf("间隔 %s 不是整分钟 — Windows 任务计划程序只支持分钟粒度,如 %dm", d, m)
	}
	return m, nil
}
