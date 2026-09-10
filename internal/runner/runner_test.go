package runner

import (
	"context"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

func TestArgvIsNotInterpretedByAShell(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/echo")
	}
	// If this went through a shell, the semicolon would start a second command.
	res := Run(context.Background(), config.Command{Cmd: []string{"echo", "a; echo b"}},
		Options{Timeout: 5 * time.Second})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if res.Output != "a; echo b" {
		t.Errorf("output = %q; the argument must reach the program verbatim", res.Output)
	}
}

// The GitHub token lives in deckhand's own environment. Deploy commands must
// never inherit it.
func TestSecretsAreNotInheritedByCommands(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	t.Setenv("DECKHAND_GITHUB_TOKEN", "super-secret")
	t.Setenv("AWS_SECRET_ACCESS_KEY", "also-secret")
	res := Run(context.Background(), config.Command{Shell: "env"}, Options{Timeout: 5 * time.Second})
	if res.Err != nil {
		t.Fatalf("run: %v", res.Err)
	}
	if strings.Contains(res.Output, "super-secret") || strings.Contains(res.Output, "also-secret") {
		t.Fatalf("command environment leaked a secret:\n%s", res.Output)
	}
	if !strings.Contains(res.Output, "PATH=") {
		t.Error("PATH must still be passed through")
	}
}

func TestExplicitEnvIsPassed(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	res := Run(context.Background(), config.Command{Shell: "echo $DECKHAND_SHA"},
		Options{Timeout: 5 * time.Second, Env: map[string]string{"DECKHAND_SHA": "abc123"}})
	if strings.TrimSpace(res.Output) != "abc123" {
		t.Errorf("output = %q, want abc123", res.Output)
	}
}

func TestTimeoutKillsTheCommand(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	start := time.Now()
	res := Run(context.Background(), config.Command{Shell: "sleep 30"},
		Options{Timeout: 300 * time.Millisecond})
	if res.Err == nil {
		t.Fatal("a command past its timeout must fail")
	}
	if !strings.Contains(res.Err.Error(), "timed out") {
		t.Errorf("error = %v, want a timeout", res.Err)
	}
	if time.Since(start) > 10*time.Second {
		t.Error("the timeout did not take effect")
	}
}

func TestRunAllStopsAtTheFirstFailure(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses /bin/sh")
	}
	marker := t.TempDir() + "/should-not-exist"
	cmds := []config.Command{
		{Shell: "exit 3"},
		{Shell: "touch " + marker},
	}
	results, err := RunAll(context.Background(), cmds, Options{Timeout: 5 * time.Second})
	if err == nil {
		t.Fatal("RunAll must report the failure")
	}
	if len(results) != 1 {
		t.Errorf("ran %d commands, want 1", len(results))
	}
	if _, statErr := os.Stat(marker); statErr == nil {
		t.Error("later commands must not run after a failure")
	}
}
