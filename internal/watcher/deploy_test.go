package watcher

import (
	"strings"
	"testing"
	"time"

	"github.com/marcwoge/deckhand/internal/deploy"
)

// A watch halts to stop a loop of failures. A deployment that passes its checks
// is the evidence that the cause is gone, so the halt has to go with it -
// otherwise the status table says "HALTED (0 failures)" and the watch stays
// stuck until someone happens to run "deckhand resume" (issue #19).
func TestSuccessfulDeployClearsAHalt(t *testing.T) {
	st := &deploy.State{
		Halted:         true,
		Failures:       3,
		LastError:      "run step exited 1",
		CurrentRelease: "/srv/app/releases/old",
		LastSHA:        "oldsha",
		LastRef:        "v1.0.0",
	}

	if !markDeployed(st, "/srv/app/releases/new", "newsha", "v1.1.0") {
		t.Error("the first success after a halt must report that it cleared one")
	}
	if st.Halted {
		t.Error("the watch is still halted after a successful deployment")
	}
	if st.Failures != 0 {
		t.Errorf("failures = %d, want 0", st.Failures)
	}
	if st.LastError != "" {
		t.Errorf("last error = %q, want it cleared", st.LastError)
	}
	// The revision that was running has to stay reachable, or "deckhand
	// rollback" has nothing to go back to.
	if st.PreviousSHA != "oldsha" || st.PreviousRelease != "/srv/app/releases/old" {
		t.Errorf("previous = %s %s, want the revision that was running",
			st.PreviousSHA, st.PreviousRelease)
	}
	if st.LastSHA != "newsha" || st.ActiveSHA != "newsha" {
		t.Errorf("last = %s, active = %s, want newsha", st.LastSHA, st.ActiveSHA)
	}
	if st.LastSuccess.IsZero() {
		t.Error("the success was not recorded")
	}

	// A second success has no halt to clear, and must not claim otherwise.
	if markDeployed(st, "/srv/app/releases/newer", "newersha", "v1.2.0") {
		t.Error("a success with no halt in place must not report clearing one")
	}
}

// The state column reports facts that are not alternatives: a deployment that
// failed with nothing to roll back to leaves the watch both halted and live on
// an unverified revision. A switch showed only the first of those, so a halted
// watch could sit in the table looking merely unverified (issue #19).
func TestWatchStateReportsEverythingThatApplies(t *testing.T) {
	cases := []struct {
		name   string
		status Status
		want   []string
		absent []string
	}{
		{
			name: "halted and unverified at once",
			status: Status{Enabled: true, Halted: true, Failures: 1,
				ActiveSHA: "762daadcc0259daab95613d1fb96e5f496293fbf"},
			want: []string{"HALTED (1 failures)", "UNVERIFIED"},
		},
		{
			name:   "halted only",
			status: Status{Enabled: true, Halted: true, Failures: 3, SHA: "abc", ActiveSHA: "abc"},
			want:   []string{"HALTED (3 failures)"},
			absent: []string{"UNVERIFIED"},
		},
		{
			name:   "disabled says nothing else",
			status: Status{Enabled: false, Halted: true, Failures: 9},
			want:   []string{"disabled"},
			absent: []string{"HALTED"},
		},
		{
			name:   "failing but not yet halted",
			status: Status{Enabled: true, Failures: 2, SHA: "abc", ActiveSHA: "abc"},
			want:   []string{"2 failures"},
			absent: []string{"HALTED"},
		},
		{
			name:   "never deployed",
			status: Status{Enabled: true},
			want:   []string{"pending"},
		},
		{
			name: "working",
			status: Status{Enabled: true, SHA: "abc", ActiveSHA: "abc",
				LastSuccess: time.Now()},
			want: []string{"ok"},
		},
	}
	for _, c := range cases {
		got := watchState(c.status)
		for _, want := range c.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: state = %q, want it to contain %q", c.name, got, want)
			}
		}
		for _, absent := range c.absent {
			if strings.Contains(got, absent) {
				t.Errorf("%s: state = %q, must not contain %q", c.name, got, absent)
			}
		}
	}
}
