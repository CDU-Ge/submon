package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"time"
)

type cliTimingKey struct{}

// 仅诊断时输出到 stderr;不会修改 statusline 的 stdout 或启动额外进程。
type statuslineTiming struct {
	started time.Time
	stages  map[string]float64
}

func newStatuslineTiming(enabled bool) *statuslineTiming {
	if !enabled {
		return nil
	}
	return &statuslineTiming{started: time.Now(), stages: make(map[string]float64)}
}

func (t *statuslineTiming) begin() time.Time {
	if t == nil {
		return time.Time{}
	}
	return time.Now()
}

func (t *statuslineTiming) end(name string, start time.Time) {
	if t != nil {
		t.stages[name+"_ms"] = float64(time.Since(start)) / float64(time.Millisecond)
	}
}

func (t *statuslineTiming) write(w io.Writer) {
	if t == nil {
		return
	}
	t.end("run", t.started)
	raw, err := json.Marshal(t.stages)
	if err == nil {
		fmt.Fprintf(w, "SUBMON_STATUSLINE_TIMING %s\n", raw)
	}
}
