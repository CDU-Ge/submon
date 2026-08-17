// Package provider adapts subscription APIs (Zhipu, MiniMax) to the unified
// model.Snapshot.
package provider

import (
	"context"
	"fmt"
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

// DetectFromBaseURL maps an ANTHROPIC_BASE_URL value to a provider.
func DetectFromBaseURL(baseURL string) (model.Provider, error) {
	switch {
	case strings.Contains(baseURL, "bigmodel.cn"), strings.Contains(baseURL, "z.ai"):
		return model.ProviderZhipu, nil
	case strings.Contains(baseURL, "minimax"):
		return model.ProviderMinimax, nil
	}
	return "", fmt.Errorf("cannot detect provider from base URL %q", baseURL)
}
