package watcher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/audit"
	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/deploy"
	"github.com/marcwoge/deckhand/internal/gh"
	"github.com/marcwoge/deckhand/internal/health"
	"github.com/marcwoge/deckhand/internal/notify"
	"github.com/marcwoge/deckhand/internal/runner"
)

// DeployNow resolves the current target for a watch and deploys it, ignoring
// the time window. It is what "deckhand deploy <name>" calls.
func (e *Engine) DeployNow(ctx context.Context, w *config.Watch, force bool) error {
	target, err := e.resolve(ctx, w)
	if err != nil {
		return err
	}
	st, err := e.loadState(w)
	if err != nil {
		return err
	}
	if !force && target.SHA == st.LastSHA {
		e.logf(w.Name, "already at %s (%s); nothing to do (use --force to redeploy)",
			deploy.Short(target.SHA), target.Ref)
		return nil
	}
	return e.deployTarget(ctx, w, target)
}

// deployTarget runs the full sequence for one revision. Only one deployment per
// watch runs at a time.
func (e *Engine) deployTarget(ctx context.Context, w *config.Watch, t *gh.Target) error {
	lock := e.lockFor(w.Name)
	lock.Lock()
	defer lock.Unlock()

	// Guards against a second deckhand process - a service and a hand-run
	// command, say - deploying the same path at the same time.
	fileLock, err := deploy.AcquireLock(e.watchStateDir(w), w.Name)
	if err != nil {
		return err
	}
	defer fileLock.Release()

	st, err := e.loadState(w)
	if err != nil {
		return err
	}
	st.LastAttempt = time.Now().UTC()

	log := func(format string, args ...interface{}) { e.logf(w.Name, format, args...) }
	d := deploy.New(w, e.watchStateDir(w), e.token, e.binary, log)
	if w.Auth == "none" {
		d = deploy.New(w, e.watchStateDir(w), "", e.binary, log)
	}

	start := time.Now()
	log("deploying %s %s (%s)", t.Kind, t.Ref, deploy.Short(t.SHA))

	err = e.runDeploySteps(ctx, w, d, st, t, log)
	duration := time.Since(start)

	if err == nil {
		st.PreviousRelease, st.PreviousSHA = st.CurrentRelease, st.LastSHA
		st.CurrentRelease = currentReleaseDir(w, d, t)
		st.LastSHA, st.LastRef = t.SHA, t.Ref
		st.ActiveSHA, st.ActiveRelease = t.SHA, st.CurrentRelease
		st.LastSuccess = time.Now().UTC()
		st.LastError = ""
		st.Failures = 0
		if err := st.Save(); err != nil {
			log("warning: could not save state: %v", err)
		}
		d.Prune(w.KeepReleases, st.CurrentRelease, st.PreviousRelease)
		log("deployed %s in %s", deploy.Short(t.SHA), duration.Round(time.Millisecond))
		_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "deploy", Result: "ok",
			Ref: t.Ref, SHA: t.SHA, Duration: duration.Round(time.Millisecond).String()})
		_ = e.notifier.Send(ctx, notify.Message{Event: "success", Watch: w.Name, Repo: w.Repo,
			Ref: t.Ref, SHA: deploy.Short(t.SHA), Host: e.host,
			Detail: fmt.Sprintf("deployed in %s", duration.Round(time.Second))})
		return nil
	}

	st.Failures++
	st.LastError = err.Error()
	failure := err

	// A failed deployment leaves the previous revision running: put it back.
	// Roll back to what was running before this attempt. A failed deployment
	// leaves the state untouched, so that is CurrentRelease - not
	// PreviousRelease, which is a further revision back and belongs to the
	// manual "deckhand rollback".
	rolledBack := false
	switch {
	case w.Rollback != "auto":
	case hasRunning(w, st):
		log("rolling back to %s", deploy.Short(st.LastSHA))
		if rbErr := e.rollbackTo(ctx, w, d, st, log, st.CurrentRelease, st.LastSHA); rbErr != nil {
			log("rollback failed: %v", rbErr)
			_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "rollback",
				Result: "failed", Message: rbErr.Error()})
		} else {
			rolledBack = true
			st.ActiveSHA, st.ActiveRelease = st.LastSHA, st.CurrentRelease
			log("rolled back to %s", deploy.Short(st.LastSHA))
			_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "rollback",
				Result: "ok", SHA: st.LastSHA})
		}
	default:
		// Nothing was running before, so the failed revision stays on disk.
		// Say so plainly rather than leaving the state claiming otherwise.
		log("nothing to roll back to; %s stays in place but did not pass its checks",
			deploy.Short(t.SHA))
		_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "rollback",
			Result: "failed", SHA: t.SHA, Message: "no previous revision to roll back to"})
	}

	if len(w.OnFailure) > 0 {
		env := e.runnerEnv(w, d, t, st, "failure", "")
		env["DECKHAND_ERROR"] = failure.Error()
		if _, ferr := runner.RunAll(ctx, w.OnFailure, runner.Options{
			WorkDir: d.WorkDir(), Timeout: time.Duration(w.CommandTimeout), Env: env,
			Logf: log,
		}); ferr != nil {
			log("on_failure command failed: %v", ferr)
		}
	}

	halted := false
	if st.Failures >= w.FailureLimit {
		st.Halted = true
		halted = true
	}
	if serr := st.Save(); serr != nil {
		log("warning: could not save state: %v", serr)
	}

	_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "deploy", Result: "failed",
		Ref: t.Ref, SHA: t.SHA, Duration: duration.Round(time.Millisecond).String(),
		Message: failure.Error()})

	event := "failure"
	detail := failure.Error()
	if rolledBack {
		event = "rollback"
		detail = failure.Error() + "\nrolled back to " + previousLabel(w, st)
	}
	_ = e.notifier.Send(ctx, notify.Message{Event: event, Watch: w.Name, Repo: w.Repo,
		Ref: t.Ref, SHA: deploy.Short(t.SHA), Host: e.host, Detail: detail})

	if halted {
		log("halted after %d consecutive failures; fix the cause and run \"deckhand resume %s\"",
			st.Failures, w.Name)
		_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "halt", Result: "ok",
			Message: fmt.Sprintf("%d consecutive failures", st.Failures)})
		_ = e.notifier.Send(ctx, notify.Message{Event: "halt", Watch: w.Name, Repo: w.Repo,
			Host: e.host, Detail: fmt.Sprintf("halted after %d consecutive failures", st.Failures)})
	}
	return failure
}

// runDeploySteps performs fetch → verify → prepare → activate → run → health.
func (e *Engine) runDeploySteps(ctx context.Context, w *config.Watch, d *deploy.Deployer,
	st *deploy.State, t *gh.Target, log func(string, ...interface{})) error {

	if err := d.Fetch(ctx); err != nil {
		return fmt.Errorf("fetch: %w", err)
	}
	if err := d.Verify(ctx, t); err != nil {
		return err
	}
	releaseDir, err := d.Prepare(ctx, t)
	if err != nil {
		return fmt.Errorf("prepare: %w", err)
	}
	if err := d.Activate(releaseDir); err != nil {
		return fmt.Errorf("activate: %w", err)
	}
	// Record what is live before running anything, so a failure cannot leave
	// the state describing a revision that is not on disk.
	st.ActiveSHA, st.ActiveRelease = t.SHA, releaseDir
	if err := st.Save(); err != nil {
		log("warning: could not save state: %v", err)
	}

	env := e.runnerEnv(w, d, t, st, "deploy", releaseDir)
	if _, err := runner.RunAll(ctx, w.Run, runner.Options{
		WorkDir: d.WorkDir(),
		Timeout: time.Duration(w.CommandTimeout),
		Env:     env,
		Logf:    log,
	}); err != nil {
		return err
	}
	if err := health.Check(ctx, w.Health, d.WorkDir(), env, log); err != nil {
		return err
	}
	return nil
}

// rollback reactivates the previous revision and re-runs the deploy commands so
// the service actually goes back, not just the files on disk.
func (e *Engine) rollbackTo(ctx context.Context, w *config.Watch, d *deploy.Deployer,
	st *deploy.State, log func(string, ...interface{}), releaseDir, sha string) error {

	if err := d.RollbackTo(ctx, releaseDir, sha); err != nil {
		return err
	}
	env := e.runnerEnv(w, d, nil, st, "rollback", releaseDir)
	env["DECKHAND_SHA"] = sha
	env["DECKHAND_SHORT_SHA"] = deploy.Short(sha)
	_, err := runner.RunAll(ctx, w.Run, runner.Options{
		WorkDir: d.WorkDir(),
		Timeout: time.Duration(w.CommandTimeout),
		Env:     env,
		Logf:    log,
	})
	return err
}

// Rollback is the manual "deckhand rollback <name>" entry point.
func (e *Engine) Rollback(ctx context.Context, w *config.Watch) error {
	lock := e.lockFor(w.Name)
	lock.Lock()
	defer lock.Unlock()

	fileLock, err := deploy.AcquireLock(e.watchStateDir(w), w.Name)
	if err != nil {
		return err
	}
	defer fileLock.Release()

	st, err := e.loadState(w)
	if err != nil {
		return err
	}
	if !hasPrevious(w, st) {
		return fmt.Errorf("no previous revision recorded for %q", w.Name)
	}
	log := func(format string, args ...interface{}) { e.logf(w.Name, format, args...) }
	token := e.token
	if w.Auth == "none" {
		token = ""
	}
	d := deploy.New(w, e.watchStateDir(w), token, e.binary, log)
	if err := e.rollbackTo(ctx, w, d, st, log, st.PreviousRelease, st.PreviousSHA); err != nil {
		return err
	}
	st.CurrentRelease, st.PreviousRelease = st.PreviousRelease, ""
	st.LastSHA, st.PreviousSHA = st.PreviousSHA, ""
	st.ActiveSHA, st.ActiveRelease = st.LastSHA, st.CurrentRelease
	st.LastSuccess = time.Now().UTC()
	st.Failures = 0
	st.LastError = ""
	_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "rollback", Result: "ok",
		SHA: st.LastSHA, Message: "manual"})
	return st.Save()
}

// ResumeWatch clears the halted flag after an operator has fixed the cause.
func (e *Engine) ResumeWatch(w *config.Watch) error {
	st, err := e.loadState(w)
	if err != nil {
		return err
	}
	st.Halted = false
	st.Failures = 0
	st.LastError = ""
	_ = e.audit.Write(audit.Event{Watch: w.Name, Repo: w.Repo, Action: "resume", Result: "ok"})
	return st.Save()
}

// hasRunning reports whether a revision is live that a failed deployment can
// be rolled back to.
func hasRunning(w *config.Watch, st *deploy.State) bool {
	if w.Strategy == config.StrategyInplace {
		return st.LastSHA != ""
	}
	if st.CurrentRelease == "" {
		return false
	}
	_, err := os.Stat(st.CurrentRelease)
	return err == nil
}

func hasPrevious(w *config.Watch, st *deploy.State) bool {
	if w.Strategy == config.StrategyInplace {
		return st.PreviousSHA != ""
	}
	if st.PreviousRelease == "" {
		return false
	}
	_, err := os.Stat(st.PreviousRelease)
	return err == nil
}

func previousLabel(w *config.Watch, st *deploy.State) string {
	if st.PreviousSHA != "" {
		return deploy.Short(st.PreviousSHA)
	}
	return strings.TrimPrefix(st.PreviousRelease, w.Path+string(os.PathSeparator))
}

func currentReleaseDir(w *config.Watch, d *deploy.Deployer, t *gh.Target) string {
	if w.Strategy == config.StrategyInplace {
		return w.Path
	}
	return filepathJoin(w.Path, "releases", t.SHA)
}

func filepathJoin(parts ...string) string {
	return filepath.Join(parts...)
}
