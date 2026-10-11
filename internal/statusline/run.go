package statusline

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/config"
)

// Options controls presentation and optional diagnostics for one invocation.
// Run is a process entry point; it sets the process clock to UTC before reads.
type Options struct {
	Color      bool
	CLIStarted time.Time
}

func Run(input io.Reader, output, diagnostics io.Writer, options Options) error {
	time.Local = time.UTC
	colorEnabled = options.Color
	refreshColorState()
	timing := newStatuslineTiming(os.Getenv("SUBMON_STATUSLINE_TIMING") == "1")
	if timing != nil {
		start := options.CLIStarted
		if start.IsZero() {
			start = timing.started
		}
		timing.end("cli", start)
		timing.stages["cli_started_unix_ms"] = float64(start.UnixMicro()) / 1000
	}
	defer timing.write(diagnostics)
	stage := timing.begin()
	cfg, err := config.Load()
	if err != nil {
		cfg = config.Default()
	}
	timing.end("config", stage)
	stage = timing.begin()
	var in statuslineInput
	terminal := false
	if f, ok := input.(*os.File); ok {
		if fi, err := f.Stat(); err == nil {
			terminal = fi.Mode()&os.ModeCharDevice != 0
		}
	}
	if !terminal {
		if raw := readStdinWithin(input, statuslineStdinBudget); len(raw) > 0 {
			_ = json.Unmarshal(raw, &in)
		}
	}
	timing.end("stdin", stage)
	stage = timing.begin()
	_, err = fmt.Fprintln(output, renderStatusline(&in, cfg, time.Now()))
	timing.end("render", stage)
	return err
}

// Execute is the standalone app's only command parser. No command framework,
// provider API adapters, login or scheduler initialization is linked here.
func Execute(args []string) int {
	start := time.Now()
	noColor := false
	for _, arg := range args {
		switch arg {
		case "statusline":
		case "--no-color":
			noColor = true
		case "--help", "-h":
			fmt.Fprintln(os.Stdout, "用法: submon.exe statusline [--no-color]")
			return 0
		default:
			fmt.Fprintln(os.Stderr, "不支持的状态栏参数:", arg)
			return 1
		}
	}
	color := !noColor && os.Getenv("NO_COLOR") == "" && !strings.EqualFold(os.Getenv("TERM"), "dumb")
	if err := Run(os.Stdin, os.Stdout, os.Stderr, Options{Color: color, CLIStarted: start}); err != nil {
		return 1
	}
	return 0
}
