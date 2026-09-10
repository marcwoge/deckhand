package notify

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/marcwoge/deckhand/internal/config"
)

type capture struct {
	mu      sync.Mutex
	bodies  []string
	headers []http.Header
	paths   []string
}

func (c *capture) handler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		c.mu.Lock()
		defer c.mu.Unlock()
		c.bodies = append(c.bodies, string(body))
		c.headers = append(c.headers, r.Header.Clone())
		c.paths = append(c.paths, r.URL.Path)
	}
}

func (c *capture) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.bodies)
}

func mustNotifier(t *testing.T, cfg config.Notify) *Notifier {
	t.Helper()
	n, err := New(cfg)
	if err != nil {
		t.Fatalf("new notifier: %v", err)
	}
	return n
}

func TestNtfyCarriesTokenPriorityAndTags(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	t.Setenv("TEST_NTFY_TOKEN", "tk_secret")
	n := mustNotifier(t, config.Notify{
		On: []string{"all"},
		Channels: []*config.Channel{{
			Type: "ntfy", URL: srv.URL + "/topic", TokenEnv: "TEST_NTFY_TOKEN",
		}},
	})

	if err := n.Send(context.Background(), Message{Event: "rollback", Watch: "shop"}); err != nil {
		t.Fatalf("send: %v", err)
	}
	if c.count() != 1 {
		t.Fatalf("got %d requests, want 1", c.count())
	}
	h := c.headers[0]
	if got := h.Get("Authorization"); got != "Bearer tk_secret" {
		t.Errorf("Authorization = %q; a protected ntfy server needs the token", got)
	}
	if got := h.Get("Priority"); got != "high" {
		t.Errorf("Priority = %q, want high for a rollback", got)
	}
	if h.Get("Tags") == "" {
		t.Error("Tags header missing; ntfy shows it as the notification icon")
	}
}

func TestNtfyPriorityCanBeOverridden(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	n := mustNotifier(t, config.Notify{
		On: []string{"all"},
		Channels: []*config.Channel{{
			Type: "ntfy", URL: srv.URL, Priority: map[string]string{"success": "urgent"},
		}},
	})
	_ = n.Send(context.Background(), Message{Event: "success", Watch: "shop"})
	if got := c.headers[0].Get("Priority"); got != "urgent" {
		t.Errorf("Priority = %q, want the configured urgent", got)
	}
}

// Several channels must all receive the message, and one broken endpoint must
// not stop the others.
func TestFanOutAndPerChannelFiltering(t *testing.T) {
	all := &capture{}
	allSrv := httptest.NewServer(all.handler())
	defer allSrv.Close()

	failures := &capture{}
	failSrv := httptest.NewServer(failures.handler())
	defer failSrv.Close()

	broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer broken.Close()

	n := mustNotifier(t, config.Notify{
		On: []string{"failure"},
		Channels: []*config.Channel{
			{Type: "webhook", URL: allSrv.URL, On: []string{"all"}},
			{Type: "ntfy", URL: failSrv.URL},
			{Type: "webhook", URL: broken.URL, On: []string{"all"}},
		},
	})

	// A success reaches only the channel that asked for everything.
	err := n.Send(context.Background(), Message{Event: "success", Watch: "shop"})
	if err == nil {
		t.Error("a failing endpoint should be reported")
	}
	if all.count() != 1 {
		t.Errorf("catch-all channel got %d messages, want 1", all.count())
	}
	if failures.count() != 0 {
		t.Errorf("failure-only channel got %d messages, want 0", failures.count())
	}

	// A failure reaches both.
	_ = n.Send(context.Background(), Message{Event: "failure", Watch: "shop"})
	if all.count() != 2 || failures.count() != 1 {
		t.Errorf("after the failure: catch-all %d (want 2), failures %d (want 1)",
			all.count(), failures.count())
	}
}

func TestWebhookSendsTheFullEvent(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	n := mustNotifier(t, config.Notify{On: []string{"all"},
		Channels: []*config.Channel{{Type: "webhook", URL: srv.URL}}})
	_ = n.Send(context.Background(), Message{Event: "failure", Watch: "shop", Repo: "acme/shop"})

	var got Message
	if err := json.Unmarshal([]byte(c.bodies[0]), &got); err != nil {
		t.Fatalf("body is not JSON: %v", err)
	}
	if got.Watch != "shop" || got.Repo != "acme/shop" || got.Time == "" {
		t.Errorf("unexpected payload: %+v", got)
	}
}

func TestSlackUsesTextField(t *testing.T) {
	c := &capture{}
	srv := httptest.NewServer(c.handler())
	defer srv.Close()

	n := mustNotifier(t, config.Notify{On: []string{"all"},
		Channels: []*config.Channel{{Type: "slack", URL: srv.URL}}})
	_ = n.Send(context.Background(), Message{Event: "halt", Watch: "shop", Detail: "3 failures"})

	var payload map[string]string
	_ = json.Unmarshal([]byte(c.bodies[0]), &payload)
	if !strings.Contains(payload["text"], "shop") || !strings.Contains(payload["text"], "3 failures") {
		t.Errorf("slack payload = %q", payload["text"])
	}
}

// Credentials must never appear in an error handed to the logs.
func TestErrorsDoNotLeakTheToken(t *testing.T) {
	n := mustNotifier(t, config.Notify{On: []string{"all"},
		Channels: []*config.Channel{{
			Type: "ntfy", URL: "http://127.0.0.1:1/topic", Token: "tk_very_secret",
		}}})
	err := n.Send(context.Background(), Message{Event: "failure", Watch: "shop"})
	if err == nil {
		t.Fatal("an unreachable endpoint must produce an error")
	}
	if strings.Contains(err.Error(), "tk_very_secret") {
		t.Fatalf("the token leaked into an error: %v", err)
	}
}
