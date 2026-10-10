package cmd

import "testing"

// TestIntervalMinutes 守住 --interval 的解析边界:Windows 任务计划程序只吃
// 整分钟(1-1439),POSIX 侧直接用原始字符串,所以这里只测能落到分钟的值。
func TestIntervalMinutes(t *testing.T) {
	cases := []struct {
		in      string
		want    int
		wantErr bool
	}{
		{in: "5m", want: 5},
		{in: "15m", want: 15},
		{in: "1m", want: 1},
		{in: "1h", want: 60},
		{in: "23h", want: 1380}, // 上限附近仍合法(1439 分钟)
		{in: " 5m ", want: 5},
		{in: "30s", wantErr: true},  // 小于 1 分钟
		{in: "0s", wantErr: true},   // 无意义
		{in: "24h", wantErr: true},  // 1440 分钟,超过 1439 上限
		{in: "90s", wantErr: true},  // 非整分钟
		{in: "5min", wantErr: true}, // 只认 Go 时长格式
		{in: "", wantErr: true},
	}
	for _, c := range cases {
		got, err := intervalMinutes(c.in)
		if c.wantErr {
			if err == nil {
				t.Errorf("intervalMinutes(%q) = %d, 期望报错", c.in, got)
			}
			continue
		}
		if err != nil {
			t.Errorf("intervalMinutes(%q) 意外报错: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("intervalMinutes(%q) = %d, 期望 %d", c.in, got, c.want)
		}
	}
}

// TestSchedulerExposesName 保证平台实现给了用户可读的机制名 —— install /
// check / notify status 的文案直接把它拼进提示里,空字符串会出现"运行  未启用"。
func TestSchedulerExposesName(t *testing.T) {
	if name := newScheduler().Name(); name == "" {
		t.Error("scheduler.Name() 不应为空")
	}
}
