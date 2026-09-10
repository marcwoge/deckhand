// Package notify delivers deployment outcomes to one or more destinations.
package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/telegram"
)

// Notifier fans a message out to every configured channel.
type Notifier struct {
	global   []string
	channels []*channel
	client   *http.Client
}

type channel struct {
	cfg   *config.Channel
	token string
	tg    *telegram.Client
}

// Message is one notification.
type Message struct {
	Event  string `json:"event"` // success, failure, rollback, halt
	Watch  string `json:"watch"`
	Repo   string `json:"repo,omitempty"`
	Ref    string `json:"ref,omitempty"`
	SHA    string `json:"sha,omitempty"`
	Host   string `json:"host,omitempty"`
	Detail string `json:"detail,omitempty"`
	Time   string `json:"time"`
}

// New builds a Notifier. Channels whose credentials cannot be read are
// reported through the returned error but the others still work: losing a
// notification channel must never stop deployments.
func New(cfg config.Notify) (*Notifier, error) {
	n := &Notifier{
		global: cfg.On,
		client: &http.Client{Timeout: 15 * time.Second},
	}
	var problems []string
	for i, c := range cfg.Channels {
		token, err := c.ResolveToken()
		if err != nil {
			problems = append(problems, fmt.Sprintf("channel #%d (%s): %v", i+1, c.Type, err))
			continue
		}
		ch := &channel{cfg: c, token: token}
		if c.Type == "telegram" {
			ch.tg = telegram.New(token)
		}
		n.channels = append(n.channels, ch)
	}
	if len(problems) > 0 {
		return n, fmt.Errorf("%s", strings.Join(problems, "; "))
	}
	return n, nil
}

// Enabled reports whether any channel wants this event.
func (n *Notifier) Enabled(event string) bool {
	if n == nil {
		return false
	}
	for _, ch := range n.channels {
		if wants(ch.cfg.Events(n.global), event) {
			return true
		}
	}
	return false
}

func wants(events []string, event string) bool {
	for _, e := range events {
		if strings.EqualFold(e, event) || e == "*" || strings.EqualFold(e, "all") {
			return true
		}
	}
	return false
}

// Send delivers the message to every channel that wants it. Channels are
// independent: one failing endpoint does not stop the others.
func (n *Notifier) Send(ctx context.Context, m Message) error {
	if n == nil || len(n.channels) == 0 {
		return nil
	}
	m.Time = time.Now().UTC().Format(time.RFC3339)

	var wg sync.WaitGroup
	errs := make([]error, len(n.channels))
	for i, ch := range n.channels {
		if !wants(ch.cfg.Events(n.global), m.Event) {
			continue
		}
		wg.Add(1)
		go func(i int, ch *channel) {
			defer wg.Done()
			errs[i] = n.sendTo(ctx, ch, m)
		}(i, ch)
	}
	wg.Wait()

	var failed []string
	for _, err := range errs {
		if err != nil {
			failed = append(failed, err.Error())
		}
	}
	if len(failed) > 0 {
		return fmt.Errorf("%s", strings.Join(failed, "; "))
	}
	return nil
}

func (n *Notifier) sendTo(ctx context.Context, ch *channel, m Message) error {
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	switch ch.cfg.Type {
	case "telegram":
		return ch.tg.Send(ctx, ch.cfg.ChatID, Plain(m, true), false)
	case "ntfy":
		return n.postNtfy(ctx, ch, m)
	case "slack":
		body, _ := json.Marshal(map[string]string{"text": Plain(m, true)})
		return n.post(ctx, ch, body, "application/json", nil)
	default: // webhook
		body, _ := json.Marshal(m)
		return n.post(ctx, ch, body, "application/json", nil)
	}
}

// ntfyPriority maps an event to how loudly the phone should announce it. A
// rollback at three in the morning should get through Do Not Disturb; a
// successful deployment should not.
var ntfyPriority = map[string]string{
	"success":  "low",
	"queue":    "low",
	"failure":  "high",
	"rollback": "high",
	"halt":     "urgent",
}

var ntfyTags = map[string]string{
	"success":  "white_check_mark",
	"failure":  "x",
	"rollback": "leftwards_arrow_with_hook",
	"halt":     "octagonal_sign",
}

func (n *Notifier) postNtfy(ctx context.Context, ch *channel, m Message) error {
	headers := map[string]string{
		"Title": "deckhand: " + m.Watch + " " + m.Event,
	}
	priority := ntfyPriority[m.Event]
	if p, ok := ch.cfg.Priority[m.Event]; ok {
		priority = p
	}
	if priority != "" {
		headers["Priority"] = priority
	}
	if tag, ok := ntfyTags[m.Event]; ok {
		headers["Tags"] = tag
	}
	// The tag already renders as an icon, so the body stays plain.
	return n.post(ctx, ch, []byte(Plain(m, false)), "text/plain", headers)
}

func (n *Notifier) post(ctx context.Context, ch *channel, body []byte, contentType string,
	headers map[string]string) error {

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ch.cfg.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("User-Agent", "deckhand")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if ch.token != "" {
		// ntfy and most self-hosted endpoints accept a bearer token; the
		// credential never goes into the URL, where it would end up in logs.
		req.Header.Set("Authorization", "Bearer "+ch.token)
	}
	resp, err := n.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s: %s", ch.cfg.Type, redact(err.Error(), ch.token))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized, resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%s: endpoint rejected the credential (HTTP %d)", ch.cfg.Type, resp.StatusCode)
	case resp.StatusCode >= 300:
		return fmt.Errorf("%s: endpoint returned HTTP %d", ch.cfg.Type, resp.StatusCode)
	}
	return nil
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// Plain renders a message as text. withIcon adds a leading emoji, which suits
// Slack and Telegram; ntfy shows its own icon from the Tags header instead.
func Plain(m Message, withIcon bool) string {
	var b strings.Builder
	if withIcon {
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
