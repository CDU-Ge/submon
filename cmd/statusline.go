package cmd

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/ge/subscription-monitor/internal/cache"
	"github.com/ge/subscription-monitor/internal/config"
	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/provider"
	"github.com/spf13/cobra"
)

// statusline renders a robbyrussell-style line for Claude Code's statusLine
// setting: dir + git branch/dirty + model + context window, extended with
// subscription quotas. It is invoked on nearly every UI render, so it must:
//   - never touch the network for quotas (cache-only),
//   - always print one line fast, even when data is stale or missing,
//   - auto-pick the provider matching the session's ANTHROPIC_BASE_URL.
//
// Example output:
// ➜ submon git:(main)✗ GLM-5.3 [12.3k/200k 6%] │ ⚡ZHIPU 5h left 97%·4h22m │ W left 80%·5d13h (age 2m)
type contextWindowInfo struct {
	TotalInputTokens  *int64 `json:"total_input_tokens"`
	ContextWindowSize *int64 `json:"context_window_size"`
	UsedPercentage    *int64 `json:"used_percentage"`
}

type statuslineInput struct {
	Workspace     struct {
		CurrentDir string `json:"current_dir"`
	} `json:"workspace"`
	Model         struct {
		Display string `json:"display_name"`
	} `json:"model"`
	ContextWindow contextWindowInfo `json:"context_window"`
}

var statuslineProvider string

var statuslineCmd = &cobra.Command{
	Use:   "statusline",
	Short: "One-line status for Claude Code (dir/git/model/context + quotas)",
	Long: "Print a robbyrussell-inspired line combining workspace dir, git branch,\n" +
		"model, context window usage and subscription quotas (cache-only reads).\n" +
		"Provider pick order: --provider > ANTHROPIC_BASE_URL detection > config default.",
	RunE: func(cmd *cobra.Command, args []string) error {
		cfg, err := config.Load()
		if err != nil {
			cfg = config.Default() // statusline must never hard-fail
		}

		// Read Claude Code's JSON payload from stdin; absent fields degrade.
		var in statuslineInput
		raw, _ := io.ReadAll(os.Stdin)
		if len(raw) > 0 {
			_ = json.Unmarshal(raw, &in)
		}

		out := cmd.OutOrStdout()
		fmt.Fprintln(out, renderStatusline(&in, cfg, time.Now()))
		return nil
	},
}

const (
	ansiReset  = "\033[0m"
	ansiGreen  = "\033[1;32m"
	ansiRed    = "\033[0;31m"
	ansiBRed   = "\033[1;31m"
	ansiCyan   = "\033[0;36m"
	ansiBlue   = "\033[1;34m"
	ansiYellow = "\033[0;33m"
	ansiDim    = "\033[2m"
)

func renderStatusline(in *statuslineInput, cfg *config.Config, now time.Time) string {
	// Model name: strip ANSI escapes and literal leftovers like "[1m]".
	modelName := stripANSI(in.Model.Display)

	cwd := in.Workspace.CurrentDir
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	dir := filepath.Base(cwd)

	var segs []string

	// ➜ dir git:(branch)✗
	prompt := ansiGreen + "➜" + ansiReset + " " + ansiCyan + dir + ansiReset
	if git := gitSegment(cwd); git != "" {
		prompt += " " + git
	}
	segs = append(segs, prompt)

	// model [ctx used/total pct%]
	if modelName != "" {
		seg := modelName
		if ctx := contextSegment(in.ContextWindow); ctx != "" {
			seg += " " + ctx
		}
		segs = append(segs, seg)
	} else if ctx := contextSegment(in.ContextWindow); ctx != "" {
		segs = append(segs, ctx)
	}

	// quotas + freshness
	if q := quotaSegment(cfg, now); q != "" {
		segs = append(segs, q)
	}

	return strings.Join(segs, " ")
}

// gitSegment renders "git:(branch)✗" in robbyrussell colors; empty outside repos.
func gitSegment(cwd string) string {
	branch, err := gitBranch(cwd)
	if err != nil || branch == "" {
		return ""
	}
	dirty := ""
	if d, err := gitDirty(cwd); err == nil && d {
		dirty = " " + ansiYellow + "✗" + ansiReset
	}
	return ansiBlue + "git:(" + ansiReset + ansiBRed + branch + ansiReset + ansiBlue + ")" + ansiReset + dirty
}

func gitBranch(cwd string) (string, error) {
	out, err := exec.Command("git", "-C", cwd, "--no-optional-locks", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func gitDirty(cwd string) (bool, error) {
	err := exec.Command("git", "-C", cwd, "--no-optional-locks", "diff", "--quiet", "HEAD").Run()
	if err == nil {
		return false, nil
	}
	if _, ok := err.(*exec.ExitError); ok {
		return true, nil // diff exited non-zero → dirty
	}
	return false, err
}

// contextSegment renders "[12.3k/200k 6%]".
func contextSegment(cw contextWindowInfo) string {
	if cw.TotalInputTokens == nil || cw.ContextWindowSize == nil || *cw.ContextWindowSize <= 0 {
		return ""
	}
	usedK := fmt.Sprintf("%.1fk", float64(*cw.TotalInputTokens)/1000)
	totalK := fmt.Sprintf("%.0fk", float64(*cw.ContextWindowSize)/1000)
	pct := ""
	if cw.UsedPercentage != nil {
		pct = fmt.Sprintf(" %d%%", *cw.UsedPercentage)
	}
	return ansiDim + "[" + ansiReset + usedK + "/" + totalK + pct + ansiDim + "]" + ansiReset
}

// quotaSegment reads the cache and renders
// "⚡ZHIPU 5h left 97%·4h22m │ W left 80%·5d13h (age 2m)".
func quotaSegment(cfg *config.Config, now time.Time) string {
	name, err := resolveStatuslineProvider(cfg)
	if err != nil {
		return "" // quotas are an optional segment; never break the line
	}
	path, err := cfg.CachePath()
	if err != nil {
		return ""
	}
	file, err := cache.New(path).Load()
	if err != nil {
		return ""
	}
	entry := file.Get(name)
	if entry == nil || entry.Snapshot == nil {
		hint := "no data, run submon status --force"
		if entry != nil && entry.LastErr != "" {
			hint = entry.LastErr
		}
		return ansiDim + "⚡" + strings.ToUpper(string(name)) + " " + hint + ansiReset
	}
	s := entry.Snapshot

	var parts []string
	if s.ShortWindow != nil {
		parts = append(parts, colorizeWindow("5h", s.ShortWindow, now, cfg))
	}
	if s.LongWindow != nil {
		parts = append(parts, colorizeWindow("W", s.LongWindow, now, cfg))
	}
	if len(parts) == 0 {
		return ""
	}
	out := ansiDim + "⚡" + ansiReset + providerBadge(s.Provider) + " " + strings.Join(parts, ansiDim+" │ "+ansiReset)

	// Freshness: age once stale, or countdown to next refresh while fresh.
	age := now.Sub(s.FetchedAt)
	ttl := time.Duration(cfg.Cache.TTLSeconds) * time.Second
	switch {
	case age > ttl:
		out += ansiYellow + fmt.Sprintf(" (age %s)", humanDuration(age)) + ansiReset
	default:
		next := ttl - age
		out += ansiDim + fmt.Sprintf(" (↻%s)", humanDuration(next)) + ansiReset
	}
	return out
}

// providerBadge renders the provider tag, uppercase for distinction.
func providerBadge(p model.Provider) string {
	label := strings.ToUpper(string(p))
	switch p {
	case model.ProviderZhipu:
		return ansiCyan + label + ansiReset
	case model.ProviderMinimax:
		return ansiYellow + label + ansiReset
	}
	return label
}

// resolveStatuslineProvider: --provider flag > ANTHROPIC_BASE_URL > config.
func resolveStatuslineProvider(cfg *config.Config) (model.Provider, error) {
	if statuslineProvider != "" {
		name := model.Provider(statuslineProvider)
		if name != model.ProviderZhipu && name != model.ProviderMinimax {
			return "", fmt.Errorf("unknown provider %q", statuslineProvider)
		}
		return name, nil
	}
	if base := os.Getenv("ANTHROPIC_BASE_URL"); base != "" {
		if name, err := provider.DetectFromBaseURL(base); err == nil {
			return name, nil
		}
	}
	if cfg.Statusline.DefaultProvider != "" {
		name := model.Provider(cfg.Statusline.DefaultProvider)
		if name == model.ProviderZhipu || name == model.ProviderMinimax {
			return name, nil
		}
	}
	return "", fmt.Errorf("cannot resolve provider")
}

// colorizeWindow renders "5h left 97%·4h22m" (left/used marker + pct + reset countdown).
func colorizeWindow(prefix string, w *model.Window, now time.Time, cfg *config.Config) string {
	if w.RemainingPct == nil && w.UsedPct == nil {
		return prefix + " n/a"
	}
	pctStr, color := "n/a", ansiGreen
	if w.RemainingPct != nil {
		pct := *w.RemainingPct
		pctStr = fmt.Sprintf("%.0f%%", pct)
		switch {
		case pct < cfg.Notify.CritBelowPct:
			color = ansiRed
		case pct < cfg.Notify.WarnBelowPct:
			color = ansiYellow
		}
	}
	out := prefix + " " + ansiDim + "left" + ansiReset + " " + color + pctStr + ansiReset
	if w.ResetAt != nil {
		out += ansiDim + "·" + humanDuration(time.Until(*w.ResetAt)) + ansiReset
	}
	return out
}

// stripANSI removes escape sequences and their literal residues (e.g. the
// "[1m]" in "GLM-5.2[1m]").
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\033':
			inEsc = true
		case inEsc && (c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z'):
			inEsc = false // end of CSI sequence
		case !inEsc:
			b.WriteByte(c)
		}
	}
	out := b.String()
	// Literal residues like "[1m]" / "[0m]" (escape already stripped elsewhere).
	out = strings.ReplaceAll(out, "[1m]", "")
	out = strings.ReplaceAll(out, "[0m]", "")
	return strings.TrimSpace(out)
}

func init() {
	statuslineCmd.Flags().StringVar(&statuslineProvider, "provider", "", "provider to display (zhipu|minimax)")
	rootCmd.AddCommand(statuslineCmd)
}
