package secret

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

func shell(script string) config.SecretSpec {
	return config.SecretSpec{What: "test.token", Command: &config.Command{Shell: script}}
}

func TestCommandProducesTheCredential(t *testing.T) {
	got, err := Resolve(context.Background(), shell("printf 'ghp_secret\\n'"))
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghp_secret" {
		t.Errorf("credential = %q, want it trimmed", got)
	}
}

// Standard output is the credential, so it must never reach an error message or
// a log line. Standard error is diagnostics and is useless if it cannot be
// shown - that is why the two are captured separately.
func TestFailureQuotesStderrButNeverStdout(t *testing.T) {
	_, err := Resolve(context.Background(),
		shell("echo ghp_leaked; echo 'vault: permission denied' >&2; exit 1"))
	if err == nil {
		t.Fatal("a failing command must be an error")
	}
	if strings.Contains(err.Error(), "ghp_leaked") {
		t.Errorf("the credential leaked into %q", err)
	}
	if !strings.Contains(err.Error(), "permission denied") {
		t.Errorf("error = %q, want the diagnostics from stderr", err)
	}
}

func TestEmptyOutputIsAnError(t *testing.T) {
	if _, err := Resolve(context.Background(), shell("true")); err == nil {
		t.Fatal("a command that produces nothing must not pass as a credential")
	}
}

// Asking Vault before every GitHub request would be wasteful and would make
// every deployment depend on the secret manager being up at that second.
func TestValueIsCachedForItsTTL(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	spec := shell(fmt.Sprintf("echo x >> %s; echo token", counter))
	spec.TTL = time.Hour

	s := New(spec, nil)
	for i := 0; i < 3; i++ {
		if _, err := s.Get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if runs := countLines(t, counter); runs != 1 {
		t.Errorf("the command ran %d times, want once inside the TTL", runs)
	}
}

func TestExpiredValueIsFetchedAgain(t *testing.T) {
	dir := t.TempDir()
	counter := filepath.Join(dir, "runs")
	spec := shell(fmt.Sprintf("echo x >> %s; echo token", counter))
	spec.TTL = time.Millisecond

	s := New(spec, nil)
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if runs := countLines(t, counter); runs != 2 {
		t.Errorf("the command ran %d times, want twice once the TTL passed", runs)
	}
}

// A secret manager that is briefly unreachable must not stop a deployment while
// the credential we already hold is still good.
func TestFailedRefreshKeepsTheCachedValue(t *testing.T) {
	dir := t.TempDir()
	flag := filepath.Join(dir, "fail")
	spec := shell(fmt.Sprintf("test -f %s && exit 1; echo token", flag))
	spec.TTL = time.Millisecond

	var logged []string
	s := New(spec, func(format string, args ...interface{}) {
		logged = append(logged, fmt.Sprintf(format, args...))
	})
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(flag, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(5 * time.Millisecond)

	got, err := s.Get(context.Background())
	if err != nil {
		t.Fatalf("a failed refresh must not fail the request: %v", err)
	}
	if got != "token" {
		t.Errorf("credential = %q, want the cached one", got)
	}
	if len(logged) == 0 {
		t.Error("a failed refresh must be reported, not swallowed")
	}
}

// With nothing cached there is nothing to fall back to, and the failure must
// surface rather than turning into an empty credential.
func TestFirstFailureIsReported(t *testing.T) {
	s := New(shell("exit 3"), nil)
	if _, err := s.Get(context.Background()); err == nil {
		t.Fatal("the first failure must be an error")
	}
}

// A credential command that hangs - gpg waiting for a passphrase, a network
// call without a timeout - must not hang deckhand with it.
func TestHangingCommandTimesOut(t *testing.T) {
	spec := shell("sleep 30")
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := Resolve(ctx, spec); err == nil {
		t.Fatal("a command that never finishes must fail")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("waited %s; the context must bound the wait", elapsed)
	}
}

// A spec without a command still resolves from file, environment or inline, so
// the same type covers every credential.
func TestSpecWithoutCommandStillResolves(t *testing.T) {
	t.Setenv("DECKHAND_TEST_SECRET", "from-env")
	got, err := Resolve(context.Background(),
		config.SecretSpec{What: "test.token", Env: "DECKHAND_TEST_SECRET"})
	if err != nil {
		t.Fatal(err)
	}
	if got != "from-env" {
		t.Errorf("credential = %q", got)
	}
}

// Describe is what check, doctor and the logs print, so it must name the source
// and never the value.
func TestDescribeNeverPrintsTheValue(t *testing.T) {
	s := New(shell("echo secret-value"), nil)
	if _, err := s.Get(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(s.Describe(), "secret-value") {
		t.Errorf("describe = %q, want only the source", s.Describe())
	}
}

func countLines(t *testing.T, path string) int {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	return len(strings.Fields(string(b)))
}
