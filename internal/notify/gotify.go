// Package notify sends push notifications via Gotify.
package notify

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Gotify pushes messages to a Gotify server.
type Gotify struct {
	URL      string // e.g. "https://gotify.example.com"
	AppToken string
	Client   *http.Client // nil → 10s timeout default
}

// Send posts a message. Gotify's API is POST /message?token=... with either
// JSON or form encoding; we use JSON for the title field.
func (g *Gotify) Send(title, message string, priority int) error {
	if g.URL == "" || g.AppToken == "" {
		return fmt.Errorf("gotify: url and app_token must be set")
	}
	endpoint := strings.TrimSuffix(g.URL, "/") + "/message?token=" + url.QueryEscape(g.AppToken)
	body := fmt.Sprintf(`{"title":%q,"message":%q,"priority":%d}`, title, message, priority)
	client := g.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Post(endpoint, "application/json", strings.NewReader(body))
	if err != nil {
		return fmt.Errorf("gotify: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("gotify: HTTP %d", resp.StatusCode)
	}
	return nil
}
