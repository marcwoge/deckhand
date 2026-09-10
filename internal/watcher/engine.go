// Package watcher polls GitHub, decides when to deploy and drives the whole
// deploy → run → health-check → rollback sequence.
package watcher

import (
	"context"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/marcwoge/deckhand/internal/audit"
	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/deploy"
	"github.com/marcwoge/deckhand/internal/gh"
	"github.com/marcwoge/deckhand/internal/notify"
)

// Engine owns every watch in a config.
type Engine struct {
	cfg      *config.Config
	client   *gh.Client
	anon     *gh.Client
	audit    *audit.Log
	notifier *notify.Notifier
	binary   string
	token    string
	stateDir string
	host     string
	logf     func(watch, format string, args ...interface{})

	mu    sync.Mutex
	locks map[string]*sync.Mutex
}

// Options configure a new Engine.
type Options struct {
	StateDir string
	Binary   string
	Logf     func(watch, format string, args ...interface{})
}

// New builds an Engine from a validated config.
func New(cfg *config.Config, o Options) (*Engine, error) {
	token, err := cfg.GitHub.ResolveToken()
	if err != nil {
		return nil, err
	}
	stateDir := o.StateDir
	if stateDir == "" {
		stateDir = cfg.Defaults.StateDir
	}
	if stateDir == "" {
		stateDir = DefaultStateDir()
	}
	logf := o.Logf
	if logf == nil {
		logf = func(string, string, ...interface{}) {}
	}
	al, err := audit.Open(stateDir)
	if err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	host, _ := os.Hostname()
	return &Engine{
		cfg:      cfg,
		client:   gh.New(cfg.GitHub.API, token),
		anon:     gh.New(cfg.GitHub.API, ""),
		audit:    al,
		notifier: notify.New(cfg.Notify),
		binary:   o.Binary,
		token:    token,
		stateDir: stateDir,
		host:     host,
		logf:     logf,
		locks:    map[string]*sync.Mutex{},
	}, nil
}

// DefaultStateDir picks a per-user or system-wide location for state and logs.
func DefaultStateDir() string {
	if d := os.Getenv("DECKHAND_STATE_DIR"); d != "" {
		return d
	}
	if os.Geteuid() == 0 {
		return systemStateDir()
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "deckhand")
	}
	return filepath.Join(os.TempDir(), "deckhand")
}

// StateDir returns the directory in use.
func (e *Engine) StateDir() string { return e.stateDir }

// AuditPath returns the audit log location.
func (e *Engine) AuditPath() string { return e.audit.Path() }

// Authenticated reports whether a GitHub token was found.
func (e *Engine) Authenticated() bool { return e.token != "" }

func (e *Engine) watchStateDir(w *config.Watch) string {
	return filepath.Join(e.stateDir, "watches", w.Name)
}

func (e *Engine) lockFor(name string) *sync.Mutex {
	e.mu.Lock()
	defer e.mu.Unlock()
	if l, ok := e.locks[name]; ok {
		return l
	}
	l := &sync.Mutex{}
	e.locks[name] = l
	return l
}

func (e *Engine) clientFor(w *config.Watch) *gh.Client {
	if w.Auth == "none" {
		return e.anon
	}
	return e.client
}

// Watch returns a configured watch by name.
func (e *Engine) Watch(name string) (*config.Watch, error) {
	for _, w := range e.cfg.Watches {
		if strings.EqualFold(w.Name, name) {
			return w, nil
		}
	}
	return nil, fmt.Errorf("no watch named %q in %s", name, e.cfg.Path)
}

// Watches returns all configured watches.
func (e *Engine) Watches() []*config.Watch { return e.cfg.Watches }

// pauseFile is the global "hold all deployments" marker.
func (e *Engine) pauseFile() string { return filepath.Join(e.stateDir, "paused") }

// Paused reports whether deployments are globally on hold.
func (e *Engine) Paused() bool {
	_, err := os.Stat(e.pauseFile())
	return err == nil
}

// Pause and Resume toggle the global hold.
func (e *Engine) Pause(reason string) error {
	if err := os.MkdirAll(e.stateDir, 0o750); err != nil {
		return err
	}
	body := fmt.Sprintf("paused at %s\n%s\n", time.Now().Format(time.RFC3339), reason)
	return os.WriteFile(e.pauseFile(), []byte(body), 0o640)
}

func (e *Engine) Resume() error {
	err := os.Remove(e.pauseFile())
	if os.IsNotExist(err) {
		return nil
	}
	return err
}

// Run starts one goroutine per enabled watch and blocks until ctx is done.
func (e *Engine) Run(ctx context.Context) error {
	enabled := 0
	var wg sync.WaitGroup
	for _, w := range e.cfg.Watches {
		if !w.IsEnabled() {
			e.logf(w.Name, "disabled in config, skipping")
			continue
		}
		enabled++
		wg.Add(1)
		go func(w *config.Watch) {
			defer wg.Done()
			e.runWatch(ctx, w)
		}(w)
	}
	if enabled == 0 {
		return fmt.Errorf("all watches are disabled")
	}
	wg.Wait()
	return ctx.Err()
}

// runWatch is the polling loop for a single watch.
func (e *Engine) runWatch(ctx context.Context, w *config.Watch) {
	interval := time.Duration(w.PollInterval)
	if w.Auth == "none" && !e.Authenticated() && interval < 5*time.Minute {
		// Unauthenticated requests are capped at 60 per hour and IP address.
		interval = 5 * time.Minute
		e.logf(w.Name, "no token available; polling every %s to stay inside the anonymous rate limit", interval)
	}
	e.logf(w.Name, "watching %s (%s), window %s, every %s",
		w.Repo, describeTrigger(w), w.Window.Describe(), interval)

	var pending *gh.Target
	var queuedNotice time.Time

	// Stagger the initial poll so many watches do not fire at the same second.
	jitter := time.Duration(rand.Int63n(int64(3 * time.Second)))
	timer := time.NewTimer(jitter)
	defer timer.Stop()

	if w.RunOnStart {
		pending = nil // resolved on the first poll; state comparison handles it
	}

	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}

		target, err := e.resolve(ctx, w)
		if err != nil {
			var rl *gh.ErrRateLimited
			if asRateLimited(err, &rl) {
				wait := time.Until(rl.Reset) + time.Second
				if wait < time.Minute {
					wait = time.Minute
				}
				e.logf(w.Name, "rate limited, sleeping %s", wait.Round(time.Second))
				timer.Reset(wait)
				continue
			}
			e.logf(w.Name, "poll failed: %v", err)
			timer.Reset(backoff(interval))
			continue
		}

		st, err := e.loadState(w)
		if err != nil {
			e.logf(w.Name, "cannot read state: %v", err)
			timer.Reset(interval)
			continue
		}

		if target.SHA != st.LastSHA || (w.RunOnStart && st.LastSuccess.IsZero()) {
			if pending == nil || pending.SHA != target.SHA {
				e.logf(w.Name, "new %s: %s (%s)", target.Kind, target.Ref, deploy.Short(target.SHA))
				queuedNotice = time.Time{}
			}
			// Coalescing: only the newest target survives until the window opens.
			pending = target
		}

		if pending != nil {
			switch {
			case st.Halted:
				if time.Since(queuedNotice) > time.Hour {
					e.logf(w.Name, "halted after %d consecutive failures; run \"deckhand resume %s\" once fixed",
						st.Failures, w.Name)
					queuedNotice = time.Now()
				}
			case e.Paused():
				if time.Since(queuedNotice) > time.Hour {
					e.logf(w.Name, "deployments globally paused; holding %s", deploy.Short(pending.SHA))
					queuedNotice = time.Now()
				}
			case !w.Window.OpenAt(time.Now()):
				next := w.Window.NextOpen(time.Now())
				if time.Since(queuedNotice) > time.Hour {
					e.logf(w.Name, "outside the deploy window; queued until %s",
						next.Format("Mon 2006-01-02 15:04 MST"))
					_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "queue",
						Result: "ok", Ref: pending.Ref, SHA: pending.SHA,
						Message: "waiting for window at " + next.Format(time.RFC3339)})
					queuedNotice = time.Now()
				}
			default:
				if err := e.deployTarget(ctx, w, pending); err != nil {
					e.logf(w.Name, "deploy failed: %v", err)
				}
				pending = nil
				queuedNotice = time.Time{}
			}
		}

		timer.Reset(withJitter(interval))
	}
}

func describeTrigger(w *config.Watch) string {
	switch w.Trigger.Type {
	case config.TriggerBranch:
		return "branch " + w.Trigger.Branch
	case config.TriggerTag:
		return "tags matching " + w.Trigger.TagMatch
	default:
		if w.Trigger.TagMatch != "" {
			return "releases matching " + w.Trigger.TagMatch
		}
		return "releases"
	}
}

// resolve asks GitHub what the watch currently points at.
func (e *Engine) resolve(ctx context.Context, w *config.Watch) (*gh.Target, error) {
	c := e.clientFor(w)
	switch w.Trigger.Type {
	case config.TriggerBranch:
		return c.BranchHead(ctx, w.Repo, w.Trigger.Branch)
	case config.TriggerTag:
		return c.LatestTag(ctx, w.Repo, w.Trigger.TagMatch)
	default:
		return c.LatestRelease(ctx, w.Repo, w.Trigger.TagMatch, w.Trigger.Prerelease)
	}
}

func (e *Engine) loadState(w *config.Watch) (*deploy.State, error) {
	return deploy.LoadState(e.watchStateDir(w), w.Name, w.Repo)
}

func withJitter(d time.Duration) time.Duration {
	if d <= 0 {
		return time.Minute
	}
	spread := d / 10
	if spread <= 0 {
		return d
	}
	return d + time.Duration(rand.Int63n(int64(spread)))
}

func backoff(base time.Duration) time.Duration {
	b := base * 2
	if b > 15*time.Minute {
		b = 15 * time.Minute
	}
	return withJitter(b)
}

func asRateLimited(err error, out **gh.ErrRateLimited) bool {
	for err != nil {
		if rl, ok := err.(*gh.ErrRateLimited); ok {
			*out = rl
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// runnerEnv builds the environment handed to the deploy commands.
func (e *Engine) runnerEnv(w *config.Watch, d *deploy.Deployer, t *gh.Target, st *deploy.State, event, releaseDir string) map[string]string {
	env := map[string]string{}
	for k, v := range e.cfg.Defaults.Env {
		env[k] = v
	}
	for k, v := range w.Env {
		env[k] = v
	}
	env["DECKHAND_WATCH"] = w.Name
	env["DECKHAND_REPO"] = w.Repo
	env["DECKHAND_EVENT"] = event
	env["DECKHAND_PATH"] = w.Path
	env["DECKHAND_WORKDIR"] = d.WorkDir()
	env["DECKHAND_SHARED_DIR"] = filepath.Join(w.Path, "shared")
	env["DECKHAND_HOST"] = e.host
	if releaseDir != "" {
		env["DECKHAND_RELEASE_DIR"] = releaseDir
	}
	if t != nil {
		env["DECKHAND_SHA"] = t.SHA
		env["DECKHAND_SHORT_SHA"] = deploy.Short(t.SHA)
		env["DECKHAND_REF"] = t.Ref
		env["DECKHAND_KIND"] = t.Kind
	}
	if st != nil && st.LastSHA != "" {
		env["DECKHAND_PREVIOUS_SHA"] = st.LastSHA
	}
	return env
}
