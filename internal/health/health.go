// Package health runs the post-deploy check that decides whether a deployment
// counts as successful.
package health

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/runner"
)

// Check runs the configured health check, retrying until it passes or the
// retry budget is used up. A nil check always succeeds.
func Check(ctx context.Context, h *config.Health, workDir string, env map[string]string,
	logf func(string, ...interface{})) error {
	if h == nil {
		return nil
	}
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	if d := time.Duration(h.InitialDelay); d > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d):
		}
	}

	var lastErr error
	attempts := h.Retries
	if attempts < 1 {
		attempts = 1
	}
	for i := 1; i <= attempts; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		lastErr = probe(ctx, h, workDir, env)
		if lastErr == nil {
			logf("health ok (attempt %d/%d)", i, attempts)
			return nil
		}
		logf("health check failed (attempt %d/%d): %v", i, attempts, lastErr)
		if i == attempts {
			break
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Duration(h.Interval)):
		}
	}
	return fmt.Errorf("health check failed after %d attempts: %w", attempts, lastErr)
}

func probe(ctx context.Context, h *config.Health, workDir string, env map[string]string) error {
	if h.HTTP != "" {
		client := &http.Client{Timeout: time.Duration(h.Timeout)}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, h.HTTP, nil)
		if err != nil {
			return err
		}
		req.Header.Set("User-Agent", "deckhand-health")
		resp, err := client.Do(req)
		if err != nil {
			return err
		}
		defer resp.Body.Close()
		if resp.StatusCode != h.ExpectStatus {
			return fmt.Errorf("got HTTP %d, want %d", resp.StatusCode, h.ExpectStatus)
		}
		return nil
	}
	res := runner.Run(ctx, *h.Cmd, runner.Options{
		WorkDir: workDir,
		Timeout: time.Duration(h.Timeout),
		Env:     env,
	})
	if res.Err != nil {
		return fmt.Errorf("exit %d: %v", res.ExitCode, res.Err)
	}
	return nil
}
