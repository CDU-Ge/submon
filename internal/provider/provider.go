// Package provider adapts subscription APIs (Zhipu, MiniMax, OpenCode, Kimi)
// to the unified model.Snapshot. Each provider declares its own URL detection
// rules; DetectFromBaseURL merges them with user-supplied overrides.
package provider

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/ge/subscription-monitor/internal/model"
)

// Source fetches a normalized snapshot from one subscription provider.
type Source interface {
	Name() model.Provider
	// Fetch returns a fresh snapshot. Implementations should use short HTTP
	// timeouts: statusline never calls this, but `status` should fail fast.
	Fetch(ctx context.Context) (*model.Snapshot, error)
}

// ErrNotConfigured marks a provider whose credentials are missing.
type ErrNotConfigured struct{ Reason string }

func (e *ErrNotConfigured) Error() string {
	return e.Reason
}

// Rule is one provider's URL detection entry.
//
// Hosts is a list of patterns matched against the URL host (after lowercasing):
//
//	"*.z.ai"       matches any subdomain of z.ai AND the bare host z.ai
//	"z.ai"         matches the host exactly
//	"kimi.com"     matches the host exactly
//
// PathPrefix narrows the match: when set, the URL path must start with this
// prefix. Useful for ambiguous hosts — e.g. `*.example.com` + `/coding/v1`
// isolates a kimi proxy that shares the example.com domain with other services.
//
// Priority breaks ties when multiple rules apply to the same URL. Higher
// wins. Default rules all sit at priority 0; user rules can override with
// any value, including negative.
type Rule struct {
	Provider   model.Provider `toml:"-"`
	Hosts      []string       `toml:"hosts,omitempty"`
	PathPrefix string         `toml:"path_prefix,omitempty"`
	Priority   int            `toml:"priority,omitempty"`
}

// DefaultDetectionRules 返回所有 provider 自己声明的检测规则聚合。
// 顺序 = 调用顺序,平级时声明在前者赢。**每个 provider 在自己文件里
// 写自己的 xxxRules(),这里只负责汇总 — 不允许把规则塞回这里。**
// 加新 provider 时改自己文件,顺道让 Build / loginRegistry 都跟上,
// 与项目其它地方同序(参见 CLAUDE.md 的"约定")。
func DefaultDetectionRules() []Rule {
	rules := make([]Rule, 0, 16)
	rules = append(rules, zhipuRules()...)
	rules = append(rules, minimaxRules()...)
	rules = append(rules, opencodeRules()...)
	rules = append(rules, kimiRules()...)
	return rules
}

// ErrUnknownProvider is returned by DetectFromBaseURL when no rule matches.
// Carries host / path / rule count so callers can surface a useful diagnostic
// (statusline silently swallows; `submon status` could print the detail).
type ErrUnknownProvider struct {
	URL       string
	Host      string
	Path      string
	RuleCount int
}

func (e *ErrUnknownProvider) Error() string {
	return fmt.Sprintf("cannot detect provider from %q (host=%q path=%q, tried %d rules)",
		e.URL, e.Host, e.Path, e.RuleCount)
}

// DetectFromBaseURL maps a base URL (the value of $ANTHROPIC_BASE_URL or any
// provider's endpoint) to a model.Provider. extraRules are merged on top of
// the default rule set; user rules win on priority, declaration order breaks
// remaining ties.
func DetectFromBaseURL(baseURL string, extraRules []Rule) (model.Provider, error) {
	u, err := url.Parse(baseURL)
	if err != nil || u.Host == "" {
		return "", fmt.Errorf("invalid base URL %q: %v", baseURL, err)
	}
	host := strings.ToLower(u.Hostname())
	path := u.Path

	all := append(DefaultDetectionRules(), extraRules...)

	var hits []Rule
	for i := range all {
		if matchRule(all[i], host, path) {
			hits = append(hits, all[i])
		}
	}
	if len(hits) == 0 {
		return "", &ErrUnknownProvider{
			URL: baseURL, Host: host, Path: path,
			RuleCount: len(all),
		}
	}
	// 排序:Priority desc,稳定排序保留声明顺序。
	sort.SliceStable(hits, func(i, j int) bool {
		return hits[i].Priority > hits[j].Priority
	})
	return hits[0].Provider, nil
}

// matchRule 单条规则匹配。Hosts 空表示"任何 host 都可以" — 与 PathPrefix
// 组合时即"按 path 推断 provider",这是用户私有代理常用的形态。
func matchRule(r Rule, host, path string) bool {
	if r.PathPrefix != "" && !strings.HasPrefix(path, r.PathPrefix) {
		return false
	}
	if len(r.Hosts) == 0 {
		return true
	}
	for _, p := range r.Hosts {
		if matchHost(p, host) {
			return true
		}
	}
	return false
}

// matchHost: "*.z.ai" 匹配 .z.ai 后缀或裸 z.ai;否则精确匹配。
// 端口由 url.Parse 在 Hostname() 阶段已剥掉,这里只看 host 部分。
func matchHost(pattern, host string) bool {
	if strings.HasPrefix(pattern, "*.") {
		suffix := pattern[1:] // ".z.ai"
		return host == pattern[2:] || strings.HasSuffix(host, suffix)
	}
	return host == pattern
}
