// Package telegram is a minimal Bot API client.
//
// Like the GitHub client it only ever makes outbound requests: updates are
// fetched with long polling rather than delivered to a webhook, so deckhand
// still needs no inbound port and works behind NAT.
package telegram

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// Client talks to the Telegram Bot API.
type Client struct {
	token string
	http  *http.Client
	api   string
}

// New returns a client for a bot token.
func New(token string) *Client {
	return &Client{
		token: token,
		api:   "https://api.telegram.org",
		// Long polling holds the request open, so the timeout must exceed the
		// poll timeout used in GetUpdates.
		http: &http.Client{Timeout: 90 * time.Second},
	}
}

// SetAPI overrides the API base URL. Used by tests.
func (c *Client) SetAPI(url string) { c.api = strings.TrimRight(url, "/") }

// Update is one incoming event; deckhand only cares about text messages.
type Update struct {
	UpdateID int      `json:"update_id"`
	Message  *Message `json:"message"`
}

// Message is a text message sent to the bot.
type Message struct {
	MessageID int    `json:"message_id"`
	Text      string `json:"text"`
	Date      int64  `json:"date"`
	Chat      Chat   `json:"chat"`
	From      User   `json:"from"`
}

// Chat identifies where a message came from.
type Chat struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
	Type     string `json:"type"`
}

// User identifies who sent a message.
type User struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

// Sent returns when the message was sent.
func (m *Message) Sent() time.Time { return time.Unix(m.Date, 0) }

type apiResponse struct {
	OK          bool            `json:"ok"`
	Result      json.RawMessage `json:"result"`
	Description string          `json:"description"`
	ErrorCode   int             `json:"error_code"`
}

func (c *Client) call(ctx context.Context, method string, payload interface{}, out interface{}) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		fmt.Sprintf("%s/bot%s/%s", c.api, c.token, method), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("telegram %s: %s", method, c.redact(err.Error()))
	}
	defer resp.Body.Close()

	data, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return err
	}
	var r apiResponse
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("telegram %s: unreadable response (HTTP %d)", method, resp.StatusCode)
	}
	if !r.OK {
		if r.ErrorCode == 401 {
			return fmt.Errorf("telegram %s: bot token rejected", method)
		}
		return fmt.Errorf("telegram %s: %s", method, r.Description)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(r.Result, out)
}

// redact keeps the bot token out of error messages, which end up in logs.
func (c *Client) redact(s string) string {
	if c.token == "" {
		return s
	}
	return strings.ReplaceAll(s, c.token, "[redacted]")
}

// maxMessage is Telegram's limit for a single message.
const maxMessage = 4096

// Send delivers a message. Text longer than the API limit is truncated rather
// than rejected, because a truncated alert beats no alert.
func (c *Client) Send(ctx context.Context, chatID, text string, monospace bool) error {
	if len(text) > maxMessage {
		text = text[:maxMessage-20] + "\n… (gekürzt)"
	}
	payload := map[string]interface{}{
		"chat_id":                  chatID,
		"text":                     text,
		"disable_web_page_preview": true,
	}
	if monospace {
		// A fixed-width block keeps the status table aligned on a phone.
		payload["text"] = "```\n" + strings.ReplaceAll(text, "```", "'''") + "\n```"
		payload["parse_mode"] = "MarkdownV2"
	}
	return c.call(ctx, "sendMessage", payload, nil)
}

// GetUpdates fetches messages newer than offset, waiting up to timeout seconds
// for one to arrive.
func (c *Client) GetUpdates(ctx context.Context, offset int, timeout int) ([]Update, error) {
	var updates []Update
	payload := map[string]interface{}{
		"timeout":         timeout,
		"allowed_updates": []string{"message"},
	}
	if offset > 0 {
		payload["offset"] = offset
	}
	if err := c.call(ctx, "getUpdates", payload, &updates); err != nil {
		return nil, err
	}
	return updates, nil
}

// VerifyToken checks the credential early, so a typo is reported at startup
// rather than at the first failed deployment.
func (c *Client) VerifyToken(ctx context.Context) (string, error) {
	var me struct {
		Username string `json:"username"`
	}
	if err := c.call(ctx, "getMe", map[string]interface{}{}, &me); err != nil {
		return "", err
	}
	return me.Username, nil
}
