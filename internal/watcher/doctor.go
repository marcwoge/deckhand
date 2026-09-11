package watcher

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/deploy"
	"github.com/marcwoge/deckhand/internal/telegram"
)

// Doctor checks everything deckhand needs but cannot validate from the
// configuration file alone: connectivity, credentials, permissions and whether
// each trigger actually resolves to something.
//
// It is strictly read-only and safe to run against a production machine.
func (e *Engine) Doctor(ctx context.Context, out io.Writer) (problems int, err error) {
	r := &report{out: out}

	r.section("Environment")
	e.checkGit(r)
	e.checkStateDir(r)

	r.section("GitHub")
	e.checkGitHub(ctx, r)

	r.section("Notifications")
	e.checkNotifications(ctx, r)

	for _, w := range e.cfg.Watches {
		r.section("Watch: " + w.Name)
		e.checkWatch(ctx, r, w)
	}

	fmt.Fprintln(out)
	switch {
	case r.failed > 0:
		fmt.Fprintf(out, "%d problem(s) and %d warning(s) found.\n", r.failed, r.warned)
	case r.warned > 0:
		fmt.Fprintf(out, "No problems, %d warning(s).\n", r.warned)
	default:
		fmt.Fprintln(out, "Everything checks out.")
	}
	return r.failed, nil
}

// report renders the check list and counts what it found.
type report struct {
	out    io.Writer
	failed int
	warned int
}

func (r *report) section(name string) { fmt.Fprintf(r.out, "\n%s\n", name) }

func (r *report) ok(format string, args ...interface{}) {
	fmt.Fprintf(r.out, "  ok    %s\n", fmt.Sprintf(format, args...))
}

func (r *report) warn(format string, args ...interface{}) {
	r.warned++
	fmt.Fprintf(r.out, "  warn  %s\n", fmt.Sprintf(format, args...))
}

func (r *report) fail(format string, args ...interface{}) {
	r.failed++
	fmt.Fprintf(r.out, "  FAIL  %s\n", fmt.Sprintf(format, args...))
}

func (e *Engine) checkGit(r *report) {
	path, err := exec.LookPath("git")
	if err != nil {
		r.fail("git is not on PATH; deckhand cannot fetch anything without it")
		return
	}
	out, err := exec.Command("git", "--version").Output()
	if err != nil {
		r.fail("git at %s does not run: %v", path, err)
		return
	}
	r.ok("%s (%s)", strings.TrimSpace(string(out)), path)
}

func (e *Engine) checkStateDir(r *report) {
	if err := os.MkdirAll(e.stateDir, 0o750); err != nil {
		r.fail("state directory %s cannot be created: %v", e.stateDir, err)
		return
	}
	probe := filepath.Join(e.stateDir, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		r.fail("state directory %s is not writable: %v", e.stateDir, err)
		return
	}
	_ = os.Remove(probe)
	r.ok("state directory %s is writable", e.stateDir)

	if os.Geteuid() == 0 {
		r.warn("running as root; a deployment worker should have its own unprivileged user")
	}
}

func (e *Engine) checkGitHub(ctx context.Context, r *report) {
	client := e.client
	if !e.Authenticated() {
		if isPublicGitHub(e.cfg.GitHub.API) {
			r.warn("no token configured: public repositories only, 60 requests/hour per IP")
		} else {
			r.warn("no token configured: only repositories this server serves anonymously")
		}
		client = e.anon
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	limit, err := client.RateLimit(ctx)
	if err != nil {
		r.fail("cannot reach %s: %v", e.cfg.GitHub.API, err)
		return
	}
	r.ok("%s reachable, %d of %d requests left (resets %s)", e.cfg.GitHub.API,
		limit.Remaining, limit.Limit, limit.Reset.Local().Format("15:04"))

	if limit.Remaining < limit.Limit/10 {
		r.warn("less than 10%% of the rate limit is left")
	}

	// Estimate the polling load against the budget.
	var perHour float64
	for _, w := range e.cfg.Watches {
		if w.IsEnabled() {
			perHour += float64(time.Hour) / float64(time.Duration(w.PollInterval))
		}
	}
	if perHour > float64(limit.Limit) {
		r.warn("polling would use about %.0f requests/hour against a limit of %d; "+
			"raise poll_interval or configure a token", perHour, limit.Limit)
	} else if perHour > 0 {
		r.ok("polling uses at most about %.0f requests/hour (unchanged answers are free)", perHour)
	}
}

func (e *Engine) checkNotifications(ctx context.Context, r *report) {
	if len(e.cfg.Notify.Channels) == 0 {
		r.warn("no notification channel configured; failures would only appear in the log")
	}
	for _, ch := range e.cfg.Notify.Channels {
		token, err := ch.ResolveToken()
		if err != nil {
			r.fail("%s: %v", ch.Type, err)
			continue
		}
		switch ch.Type {
		case "telegram":
			cctx, cancel := context.WithTimeout(ctx, 15*time.Second)
			username, err := telegram.New(token).VerifyToken(cctx)
			cancel()
			if err != nil {
				r.fail("telegram: %v", err)
				continue
			}
			extra := ""
			if ch.Commands {
				extra = ", accepting commands from chat " + ch.ChatID
			}
			r.ok("telegram bot @%s reachable%s", username, extra)
		case "ntfy":
			if token == "" && strings.Contains(ch.URL, "ntfy.sh") {
				r.warn("ntfy topic %s is public: anyone who guesses the name can read "+
					"your deployments and post to it", ch.URL)
			} else {
				r.ok("ntfy -> %s%s", ch.URL, authNote(token))
			}
		default:
			r.ok("%s -> %s%s", ch.Type, ch.URL, authNote(token))
		}
	}
	if e.cfg.Heartbeat.Enabled() {
		r.ok("heartbeat every %s (not pinged here, so a dead worker cannot look alive)",
			e.cfg.Heartbeat.Interval)
	} else {
		r.warn("no heartbeat configured; if deckhand itself dies you would not be told")
	}
}

func authNote(token string) string {
	if token != "" {
		return " (authenticated)"
	}
	return ""
}

func (e *Engine) checkWatch(ctx context.Context, r *report, w *config.Watch) {
	if !w.IsEnabled() {
		r.warn("disabled in the configuration")
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	private, err := e.clientFor(w).Repository(ctx, w.Repo)
	if err != nil {
		r.fail("%s: %v", w.Repo, err)
	} else {
		visibility := "public"
		if private {
			visibility = "private"
		}
		r.ok("%s readable (%s)", w.Repo, visibility)
	}

	target, err := e.resolve(ctx, w)
	if err != nil {
		r.fail("trigger does not resolve: %v", err)
	} else {
		r.ok("trigger resolves to %s %s (%s)", target.Kind, target.Ref, deploy.Short(target.SHA))
		st, err := e.loadState(w)
		if err == nil && st.LastSHA != "" && st.LastSHA != target.SHA {
			r.warn("deployed revision %s is behind %s", deploy.Short(st.LastSHA), deploy.Short(target.SHA))
		}
	}

	e.checkPath(r, w)

	if w.Health != nil && w.Health.HTTP != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, w.Health.HTTP, nil)
		if err != nil {
			r.fail("health check url is invalid: %v", err)
		} else {
			resp, err := (&http.Client{Timeout: time.Duration(w.Health.Timeout)}).Do(req)
			switch {
			case err != nil:
				r.warn("health check %s is not answering right now: %v", w.Health.HTTP, err)
			case resp.StatusCode != w.Health.ExpectStatus:
				resp.Body.Close()
				r.warn("health check %s returns HTTP %d, deployments expect %d",
					w.Health.HTTP, resp.StatusCode, w.Health.ExpectStatus)
			default:
				resp.Body.Close()
				r.ok("health check %s answers %d", w.Health.HTTP, resp.StatusCode)
			}
		}
	} else if w.Health != nil {
		r.ok("health check runs %s", w.Health.Cmd.String())
	} else {
		r.warn("no health check; a broken deployment would not be detected or rolled back")
	}

	if len(w.Run) > 0 && w.Run[0].Shell == "" && len(w.Run[0].Cmd) > 0 {
		if _, err := exec.LookPath(w.Run[0].Cmd[0]); err != nil {
			r.warn("first command %q was not found on PATH", w.Run[0].Cmd[0])
		}
	}
}

// checkPath verifies the deployment directory can actually be written to,
// which is the failure people hit most often after setting up a service user.
func (e *Engine) checkPath(r *report, w *config.Watch) {
	if err := os.MkdirAll(w.Path, 0o755); err != nil {
		r.fail("deployment path %s cannot be created: %v", w.Path, err)
		return
	}
	probe := filepath.Join(w.Path, ".doctor-probe")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		r.fail("deployment path %s is not writable by this user: %v", w.Path, err)
		return
	}
	_ = os.Remove(probe)

	if w.Strategy == config.StrategyReleases {
		// The current link is what makes an atomic switch possible.
		link := filepath.Join(w.Path, ".doctor-link")
		if err := deploy.ProbeLink(link, w.Path); err != nil {
			r.fail("cannot create the \"current\" link in %s: %v; "+
				"enable Developer Mode on Windows or use strategy: inplace", w.Path, err)
			return
		}
	}
	r.ok("deployment path %s is writable", w.Path)
}
