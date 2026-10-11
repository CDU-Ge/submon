// Package provider adapts subscription APIs to model.Snapshot.
// URL detection is shared with the lightweight renderer through routing.
package provider

import (
	"context"

	"github.com/ge/subscription-monitor/internal/model"
	"github.com/ge/subscription-monitor/internal/routing"
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

// Rule and the detector retain the provider API while sharing pure routing.
type Rule = routing.Rule
type ErrUnknownProvider = routing.ErrUnknownProvider

func DefaultDetectionRules() []Rule { return routing.DefaultDetectionRules() }
func DetectFromBaseURL(baseURL string, extra []Rule) (model.Provider, error) {
	return routing.DetectFromBaseURL(baseURL, extra)
}
