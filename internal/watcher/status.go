package watcher

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/marcwoge/deckhand/internal/audit"
	"github.com/marcwoge/deckhand/internal/deploy"
)

// Status is the human-facing summary of one watch.
type Status struct {
	Name        string    `json:"name"`
	Repo        string    `json:"repo"`
	Trigger     string    `json:"trigger"`
	Window      string    `json:"window"`
	Path        string    `json:"path"`
	SHA         string    `json:"sha,omitempty"`
	Ref         string    `json:"ref,omitempty"`
	LastSuccess time.Time `json:"last_success,omitempty"`
	LastAttempt time.Time `json:"last_attempt,omitempty"`
	Failures    int       `json:"failures"`
	Halted      bool      `json:"halted"`
	Enabled     bool      `json:"enabled"`
	LastError   string    `json:"last_error,omitempty"`
}

// Status collects the state of every watch.
func (e *Engine) Status() ([]Status, error) {
	var out []Status
	for _, w := range e.cfg.Watches {
		st, err := e.loadState(w)
		if err != nil {
			return nil, err
		}
		out = append(out, Status{
			Name: w.Name, Repo: w.Repo, Trigger: describeTrigger(w),
			Window: w.Window.Describe(), Path: w.Path,
			SHA: st.LastSHA, Ref: st.LastRef, LastSuccess: st.LastSuccess,
			LastAttempt: st.LastAttempt, Failures: st.Failures, Halted: st.Halted,
			Enabled: w.IsEnabled(), LastError: st.LastError,
		})
	}
	return out, nil
}

// PrintStatus renders the status table.
func (e *Engine) PrintStatus(w *os.File, asJSON bool) error {
	sts, err := e.Status()
	if err != nil {
		return err
	}
	if asJSON {
		enc := json.NewEncoder(w)
		enc.SetIndent("", "  ")
		return enc.Encode(map[string]interface{}{
			"paused":        e.Paused(),
			"state_dir":     e.stateDir,
			"authenticated": e.Authenticated(),
			"watches":       sts,
		})
	}
	if e.Paused() {
		fmt.Fprintln(w, "deployments are PAUSED globally (deckhand resume)")
	}
	if !e.Authenticated() {
		fmt.Fprintln(w, "no GitHub token configured: public repositories only, 60 requests/hour")
	}
	tw := tabwriter.NewWriter(w, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "WATCH\tREPO\tTRIGGER\tREVISION\tLAST SUCCESS\tSTATE")
	for _, s := range sts {
		rev := "-"
		if s.SHA != "" {
			rev = deploy.Short(s.SHA)
			if s.Ref != "" {
				rev = s.Ref + " " + rev
			}
		}
		last := "never"
		if !s.LastSuccess.IsZero() {
			last = humanAgo(s.LastSuccess)
		}
		state := "ok"
		switch {
		case !s.Enabled:
			state = "disabled"
		case s.Halted:
			state = fmt.Sprintf("HALTED (%d failures)", s.Failures)
		case s.Failures > 0:
			state = fmt.Sprintf("%d failures", s.Failures)
		case s.LastSuccess.IsZero():
			state = "pending"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n", s.Name, s.Repo, s.Trigger, rev, last, state)
	}
	if err := tw.Flush(); err != nil {
		return err
	}
	for _, s := range sts {
		if s.LastError != "" && (s.Halted || s.Failures > 0) {
			fmt.Fprintf(w, "\n%s: last error: %s\n", s.Name, s.LastError)
		}
	}
	return nil
}

func humanAgo(t time.Time) string {
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02 15:04")
	}
}

// PrintHistory replays the audit log, newest last.
func (e *Engine) PrintHistory(out *os.File, watch string, limit int) error {
	f, err := os.Open(e.audit.Path())
	if os.IsNotExist(err) {
		fmt.Fprintln(out, "no history yet")
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()

	var events []audit.Event
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		var ev audit.Event
		if err := json.Unmarshal(sc.Bytes(), &ev); err != nil {
			continue
		}
		if watch != "" && !strings.EqualFold(ev.Watch, watch) {
			continue
		}
		events = append(events, ev)
	}
	if err := sc.Err(); err != nil {
		return err
	}
	if limit > 0 && len(events) > limit {
		events = events[len(events)-limit:]
	}
	if len(events) == 0 {
		fmt.Fprintln(out, "no matching history")
		return nil
	}
	tw := tabwriter.NewWriter(out, 0, 4, 2, ' ', 0)
	fmt.Fprintln(tw, "TIME\tWATCH\tACTION\tRESULT\tREVISION\tDETAIL")
	for _, ev := range events {
		rev := ev.Ref
		if ev.SHA != "" {
			rev = strings.TrimSpace(rev + " " + deploy.Short(ev.SHA))
		}
		detail := ev.Message
		if ev.Duration != "" {
			detail = strings.TrimSpace(ev.Duration + " " + detail)
		}
		if len(detail) > 80 {
			detail = detail[:77] + "..."
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\n",
			ev.Time.Local().Format("2006-01-02 15:04:05"), ev.Watch, ev.Action, ev.Result,
			rev, strings.ReplaceAll(detail, "\n", " "))
	}
	return tw.Flush()
}
