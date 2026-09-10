package heartbeat

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

func TestDisabledWithoutURL(t *testing.T) {
	if p := New(config.Heartbeat{}, "host", nil); p != nil {
		t.Fatal("no url means no heartbeat")
	}
}

func TestPingsImmediatelyAndRepeatedly(t *testing.T) {
	var hits int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("method = %s, want GET", r.Method)
		}
		atomic.AddInt32(&hits, 1)
	}))
	defer srv.Close()

	cfg := config.Heartbeat{URL: srv.URL + "/ping/abc", Interval: config.Duration(50 * time.Millisecond),
		Timeout: config.Duration(2 * time.Second), Method: "GET"}
	p := New(cfg, "web-01", nil)

	ctx, cancel := context.WithTimeout(context.Background(), 180*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	// One at startup plus at least two ticks: the first ping must not wait for
	// the first interval, or an outage right after a restart goes unnoticed.
	if got := atomic.LoadInt32(&hits); got < 3 {
		t.Errorf("got %d pings, want at least 3", got)
	}
}

func TestFailuresAreLoggedButDoNotStopTheLoop(t *testing.T) {
	var logged []string
	logf := func(format string, args ...interface{}) {
		logged = append(logged, format)
	}
	cfg := config.Heartbeat{URL: "http://127.0.0.1:1/ping/secret-uuid",
		Interval: config.Duration(40 * time.Millisecond), Timeout: config.Duration(20 * time.Millisecond),
		Method: "GET"}
	p := New(cfg, "web-01", logf)

	ctx, cancel := context.WithTimeout(context.Background(), 150*time.Millisecond)
	defer cancel()
	p.Run(ctx) // must return on ctx, not hang or panic

	joined := strings.Join(logged, " ")
	if !strings.Contains(joined, "failed") {
		t.Errorf("a broken heartbeat endpoint must be reported, logged: %v", logged)
	}
	if strings.Contains(joined, "secret-uuid") {
		t.Error("the ping path is a credential and must not be logged")
	}
}

// For healthchecks.io and Uptime Kuma the path is the secret.
func TestRedactPathKeepsTheHostVisible(t *testing.T) {
	got := redactPath("Get \"https://hc-ping.com/8f3a-secret-uuid\": connection refused")
	if strings.Contains(got, "8f3a-secret-uuid") {
		t.Errorf("path not redacted: %s", got)
	}
	if !strings.Contains(got, "hc-ping.com") {
		t.Errorf("the host should stay visible so the operator knows what failed: %s", got)
	}
}

func TestPostSendsABody(t *testing.T) {
	body := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 256)
		n, _ := r.Body.Read(buf)
		select {
		case body <- string(buf[:n]):
		default:
		}
	}))
	defer srv.Close()

	cfg := config.Heartbeat{URL: srv.URL, Interval: config.Duration(time.Hour),
		Timeout: config.Duration(2 * time.Second), Method: "POST"}
	p := New(cfg, "web-01", nil)
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	go p.Run(ctx)

	select {
	case got := <-body:
		if !strings.Contains(got, "web-01") {
			t.Errorf("POST body = %q, want the hostname", got)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no ping arrived")
	}
}
