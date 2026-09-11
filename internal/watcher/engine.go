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
	"github.com/marcwoge/deckhand/internal/heartbeat"
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

	// gen is the currently running set of goroutines. A reload stops the old
	// generation and starts a new one.
	genMu sync.Mutex
	gen   *generation
}

// generation is one running set of watch, heartbeat and command goroutines.
type generation struct {
	cancel context.CancelFunc
	wg     sync.WaitGroup
	names  []string
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
	al, err := audit.Open(stateDir, cfg.Defaults.Audit.MaxSize.B(10<<20), cfg.Defaults.Audit.Keep)
	if err != nil {
		return nil, fmt.Errorf("audit log: %w", err)
	}
	host, _ := os.Hostname()
	notifier, notifyErr := notify.New(cfg.Notify)
	e := &Engine{
		cfg:      cfg,
		client:   gh.New(cfg.GitHub.API, token),
		anon:     gh.New(cfg.GitHub.API, ""),
		audit:    al,
		notifier: notifier,
		binary:   o.Binary,
		token:    token,
		stateDir: stateDir,
		host:     host,
		logf:     logf,
		locks:    map[string]*sync.Mutex{},
	}
	if notifyErr != nil {
		// A broken notification channel is worth complaining about loudly, but
		// it must never stop deployments from running.
		logf("notify", "warning: %v", notifyErr)
	}
	return e, nil
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

// Run starts one goroutine per enabled watch, plus the heartbeat and the
// Telegram command listener, and blocks until ctx is done.
func (e *Engine) Run(ctx context.Context) error {
	if err := e.start(ctx); err != nil {
		return err
	}
	<-ctx.Done()
	e.stop()
	return ctx.Err()
}

// start launches a new generation of goroutines.
func (e *Engine) start(ctx context.Context) error {
	enabled := 0
	for _, w := range e.cfg.Watches {
		if w.IsEnabled() {
			enabled++
		}
	}
	if enabled == 0 {
		return fmt.Errorf("all watches are disabled")
	}

	genCtx, cancel := context.WithCancel(ctx)
	g := &generation{cancel: cancel}

	if p := heartbeat.New(e.cfg.Heartbeat, e.host, func(f string, a ...interface{}) {
		e.logf("heartbeat", f, a...)
	}); p != nil {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			p.Run(genCtx)
		}()
	}
	if e.cfg.Notify.CommandChannel() != nil {
		g.wg.Add(1)
		go func() {
			defer g.wg.Done()
			e.runCommandBot(genCtx)
		}()
	}
	for _, w := range e.cfg.Watches {
		if !w.IsEnabled() {
			e.logf(w.Name, "disabled in config, skipping")
			continue
		}
		g.names = append(g.names, w.Name)
		g.wg.Add(1)
		go func(w *config.Watch) {
			defer g.wg.Done()
			e.runWatch(genCtx, w)
		}(w)
	}

	e.genMu.Lock()
	e.gen = g
	e.genMu.Unlock()
	return nil
}

// stop ends the current generation, waiting for any deployment that is already
// running rather than killing it half way through.
func (e *Engine) stop() {
	e.genMu.Lock()
	g := e.gen
	e.gen = nil
	e.genMu.Unlock()
	if g == nil {
		return
	}
	// Taking each watch lock waits for an in-flight deployment to finish.
	for _, name := range g.names {
		lock := e.lockFor(name)
		lock.Lock()
		lock.Unlock() //nolint:staticcheck // we only want to wait, not to hold
	}
	g.cancel()
	g.wg.Wait()
}

// Reload re-reads the configuration file and restarts everything with it.
//
// An invalid configuration is reported and otherwise ignored: the worker keeps
// running with what it had, because stopping deployments because of a typo is
// worse than deploying slightly stale settings.
func (e *Engine) Reload(ctx context.Context) error {
	newCfg, err := config.Load(e.cfg.Path)
	if err != nil {
		return fmt.Errorf("configuration not reloaded, keeping the running one: %w", err)
	}
	token, err := newCfg.GitHub.ResolveToken()
	if err != nil {
		return fmt.Errorf("configuration not reloaded, keeping the running one: %w", err)
	}
	notifier, notifyErr := notify.New(newCfg.Notify)
	if notifyErr != nil {
		e.logf("notify", "warning: %v", notifyErr)
	}

	e.logf("config", "reloading %s", newCfg.Path)
	e.stop()

	e.cfg = newCfg
	e.token = token
	e.client = gh.New(newCfg.GitHub.API, token)
	e.anon = gh.New(newCfg.GitHub.API, "")
	e.notifier = notifier

	if err := e.start(ctx); err != nil {
		return fmt.Errorf("reloaded configuration could not be started: %w", err)
	}
	e.logf("config", "reloaded: %d watches", len(newCfg.Watches))
	return nil
}

// runWatch is the polling loop for a single watch.
func (e *Engine) runWatch(ctx context.Context, w *config.Watch) {
	interval := time.Duration(w.PollInterval)
	// github.com caps unauthenticated requests at 60 per hour and IP address,
	// which one watch at a minute would exhaust. Other endpoints - GitHub
	// Enterprise, a mirror, a test server - have their own limits, so the
	// configured interval is honoured there.
	if w.Auth == "none" && !e.Authenticated() && interval < 5*time.Minute &&
		isPublicGitHub(e.cfg.GitHub.API) {
		interval = 5 * time.Minute
		e.logf(w.Name, "no token available; polling every %s to stay inside github.com's anonymous rate limit", interval)
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

// isPublicGitHub reports whether the configured API is github.com's, whose
// anonymous rate limit is the one worth slowing down for.
func isPublicGitHub(api string) bool {
	return api == "" || api == "https://api.github.com" || api == "http://api.github.com"
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
