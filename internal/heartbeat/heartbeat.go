// Package heartbeat tells an outside service that deckhand is still alive.
//
// Notifications only fire when something goes wrong, which leaves one blind
// spot: a crashed worker, a powered-off machine or a broken network produce no
// notification at all, and silence looks exactly like everything being fine.
// A dead-man's-switch closes that gap - if the ping stops arriving, the
// service raises the alarm.
package heartbeat

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

// Pinger sends the periodic ping.
type Pinger struct {
	cfg    config.Heartbeat
	client *http.Client
	logf   func(format string, args ...interface{})
	host   string
}

// New returns a Pinger, or nil when no heartbeat is configured.
func New(cfg config.Heartbeat, host string, logf func(string, ...interface{})) *Pinger {
	if !cfg.Enabled() {
		return nil
	}
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &Pinger{
		cfg:    cfg,
		client: &http.Client{Timeout: time.Duration(cfg.Timeout)},
		logf:   logf,
		host:   host,
	}
}

// Run pings immediately and then on every interval until ctx is done.
func (p *Pinger) Run(ctx context.Context) {
	if p == nil {
		return
	}
	interval := time.Duration(p.cfg.Interval)
	p.logf("heartbeat: pinging %s every %s", redactPath(p.cfg.URL), interval)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	failures := 0
	for {
		if err := p.ping(ctx); err != nil {
			failures++
			// Log the first failure and then only occasionally: a heartbeat
			// endpoint that is down must not drown out the deployment log.
			if failures == 1 || failures%10 == 0 {
				p.logf("heartbeat: ping failed (%d in a row): %v", failures, err)
			}
		} else if failures > 0 {
			p.logf("heartbeat: ping succeeded again after %d failures", failures)
			failures = 0
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (p *Pinger) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, time.Duration(p.cfg.Timeout))
	defer cancel()

	var body *strings.Reader
	if p.cfg.Method == http.MethodPost {
		body = strings.NewReader("deckhand alive on " + p.host)
	} else {
		body = strings.NewReader("")
	}
	req, err := http.NewRequestWithContext(ctx, p.cfg.Method, p.cfg.URL, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "deckhand")
	if p.cfg.Method == http.MethodPost {
		req.Header.Set("Content-Type", "text/plain")
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return fmt.Errorf("%s", redactPath(err.Error()))
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return fmt.Errorf("endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

// redactPath hides the path of the ping URL. For healthchecks.io and Uptime
// Kuma that path is the secret: anyone who has it can fake the heartbeat.
func redactPath(s string) string {
	for _, scheme := range []string{"https://", "http://"} {
		i := strings.Index(s, scheme)
		if i < 0 {
			continue
		}
		rest := s[i+len(scheme):]
		end := strings.IndexAny(rest, " \"")
		if end < 0 {
			end = len(rest)
		}
		url := rest[:end]
		if slash := strings.IndexByte(url, '/'); slash >= 0 {
			s = s[:i+len(scheme)] + url[:slash] + "/…" + rest[end:]
		}
	}
	return s
}
