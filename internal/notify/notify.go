// Package notify sends deployment outcomes to a webhook.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

// Notifier posts messages to the configured endpoint.
type Notifier struct {
	cfg    config.Notify
	client *http.Client
}

// New returns a Notifier. It is safe to use when no webhook is configured;
// every send then becomes a no-op.
func New(cfg config.Notify) *Notifier {
	return &Notifier{cfg: cfg, client: &http.Client{Timeout: 15 * time.Second}}
}

// Message is one notification.
type Message struct {
	Event  string `json:"event"` // success, failure, rollback, skip, halt
	Watch  string `json:"watch"`
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Host   string `json:"host,omitempty"`
	Detail string `json:"detail,omitempty"`
	Time   string `json:"time"`
}

// Enabled reports whether this event type should be sent.
func (n *Notifier) Enabled(event string) bool {
	if n == nil || n.cfg.Webhook == "" {
		return false
	}
	for _, e := range n.cfg.On {
		if strings.EqualFold(e, event) || e == "*" || strings.EqualFold(e, "all") {
			return true
		}
	}
	return false
}

// Send delivers the message if the event type is enabled.
func (n *Notifier) Send(ctx context.Context, m Message) error {
	if !n.Enabled(m.Event) {
		return nil
	}
	m.Time = time.Now().UTC().Format(time.RFC3339)

	var body []byte
	contentType := "application/json"
	switch strings.ToLower(n.cfg.Format) {
	case "slack":
		payload := map[string]string{"text": plain(m)}
		body, _ = json.Marshal(payload)
	case "ntfy":
		body = []byte(plain(m))
		contentType = "text/plain"
	default:
		body, _ = json.Marshal(m)
	}

	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, n.cfg.Webhook, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "deckhand")
	if strings.EqualFold(n.cfg.Format, "ntfy") {
		req.Header.Set("Title", "deckhand: "+m.Watch+" "+m.Event)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("notification endpoint returned %d", resp.StatusCode)
	}
	return nil
}

func plain(m Message) string {
	var b strings.Builder
	switch m.Event {
	case "success":
		b.WriteString("✅ ")
	case "failure":
		b.WriteString("❌ ")
	case "rollback":
		b.WriteString("↩️ ")
	case "halt":
		b.WriteString("⛔ ")
	default:
		b.WriteString("ℹ️ ")
	}
	fmt.Fprintf(&b, "deckhand %s: %s", m.Event, m.Watch)
	if m.Repo != "" {
		fmt.Fprintf(&b, " (%s", m.Repo)
		if m.Ref != "" {
			fmt.Fprintf(&b, " @ %s", m.Ref)
		}
		b.WriteString(")")
	}
	if m.Host != "" {
		fmt.Fprintf(&b, " on %s", m.Host)
	}
	if m.Detail != "" {
		fmt.Fprintf(&b, "\n%s", m.Detail)
	}
	return b.String()
}
