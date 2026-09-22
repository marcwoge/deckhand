// Package secret resolves a credential that comes from a command, so any secret
// manager works without Deckhand knowing about it: pass, gopass, the 1Password
// CLI, Bitwarden, Vault, aws secretsmanager, sops, age, a company script.
//
// Encrypting a credential with a key that sits next to it is not meaningfully
// better than chmod 600 - Deckhand must decrypt it unattended, so the key is on
// the same machine. What genuinely helps is fetching the credential from
// something that can revoke and audit, or binding it to hardware. This package
// is the first of those; systemd-creds (see docs/*/security.md) is the second.
package secret

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

// defaultTTL is how long a value from a command is reused. Asking Vault before
// every GitHub request would be wasteful and would make every deployment depend
// on the secret manager being up at that exact second.
const defaultTTL = time.Hour

// commandTimeout bounds one execution. A credential command that hangs - gpg
// waiting for a passphrase, a network call with no timeout - must not hang
// Deckhand with it.
const commandTimeout = 30 * time.Second

// maxStderr caps how much of a failing command's diagnostics is quoted.
const maxStderr = 400

// Source produces a credential on demand and caches it.
type Source struct {
	spec config.SecretSpec
	ttl  time.Duration

	mu      sync.Mutex
	value   string
	fetched time.Time
	// logf reports a refresh that failed while a usable value was still cached.
	logf func(format string, args ...interface{})
}

// New returns a source for a credential spec. Nothing is executed yet.
func New(spec config.SecretSpec, logf func(format string, args ...interface{})) *Source {
	ttl := spec.TTL
	if ttl <= 0 {
		ttl = defaultTTL
	}
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &Source{spec: spec, ttl: ttl, logf: logf}
}

// Get returns the credential, running the command at most once per TTL.
func (s *Source) Get(ctx context.Context) (string, error) {
	if !s.spec.FromCommand() {
		return s.spec.Resolve()
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.value != "" && time.Since(s.fetched) < s.ttl {
		return s.value, nil
	}

	value, err := run(ctx, *s.spec.Command, s.spec.What)
	if err != nil {
		// A secret manager that is briefly unreachable must not stop a
		// deployment while the credential we hold is still good.
		if s.value != "" {
			s.logf("%s: keeping the cached credential: %v", s.spec.What, err)
			return s.value, nil
		}
		return "", err
	}
	s.value = value
	s.fetched = time.Now()
	return value, nil
}

// Describe names the source without printing the value.
func (s *Source) Describe() string { return s.spec.Describe() }

// Resolve reads a credential once, running a command if that is the source.
// Used for credentials that are read at startup and not refreshed.
func Resolve(ctx context.Context, spec config.SecretSpec) (string, error) {
	if !spec.FromCommand() {
		return spec.Resolve()
	}
	return run(ctx, *spec.Command, spec.What)
}

// run executes a credential command and returns its standard output.
//
// It does not use internal/runner, which merges stdout and stderr: here the
// two must stay apart, because stdout is the secret and must never be logged,
// while stderr is the diagnostics and is useless if it cannot be shown.
func run(ctx context.Context, c config.Command, what string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, commandTimeout)
	defer cancel()

	var cmd *exec.Cmd
	switch {
	case c.Shell != "":
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", c.Shell)
		} else {
			cmd = exec.CommandContext(ctx, "/bin/sh", "-c", c.Shell)
		}
	case len(c.Cmd) > 0:
		cmd = exec.CommandContext(ctx, c.Cmd[0], c.Cmd[1:]...)
	default:
		return "", fmt.Errorf("%s: empty credential command", what)
	}
	cmd.Dir = c.Dir

	var out, errOut bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errOut
	// Do not wait forever for a child that holds the output pipe open after the
	// context is cancelled.
	cmd.WaitDelay = 2 * time.Second

	err := cmd.Run()
	if ctx.Err() != nil {
		return "", fmt.Errorf("%s: %s did not finish: %v", what, c.Label(), ctx.Err())
	}
	if err != nil {
		// Only stderr is quoted. Standard output is the credential.
		if diag := trim(errOut.String()); diag != "" {
			return "", fmt.Errorf("%s: %s failed: %v: %s", what, c.Label(), err, diag)
		}
		return "", fmt.Errorf("%s: %s failed: %v (it wrote nothing to stderr; "+
			"run it by hand to see why)", what, c.Label(), err)
	}

	value := strings.TrimSpace(out.String())
	if value == "" {
		return "", fmt.Errorf("%s: %s produced no credential on standard output",
			what, c.Label())
	}
	return value, nil
}

func trim(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > maxStderr {
		s = s[:maxStderr] + "…"
	}
	return strings.ReplaceAll(s, "\n", " ")
}
