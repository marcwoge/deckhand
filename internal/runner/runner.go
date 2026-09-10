// Package runner executes the commands configured for a watch.
//
// Two rules keep this safe: commands come from the local config file, never
// from the watched repository, and they are executed as an argv vector without
// a shell unless the operator explicitly asks for one.
package runner

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
)

// Result describes one finished command.
type Result struct {
	Command  string
	ExitCode int
	Duration time.Duration
	Output   string
	Err      error
}

// Options control a single execution.
type Options struct {
	WorkDir string
	Timeout time.Duration
	Env     map[string]string
	// Logf receives streamed status lines.
	Logf func(format string, args ...interface{})
}

// Run executes one command and returns its result.
func Run(ctx context.Context, c config.Command, o Options) Result {
	timeout := o.Timeout
	if c.Timeout != 0 {
		timeout = time.Duration(c.Timeout)
	}
	if timeout <= 0 {
		timeout = 10 * time.Minute
	}

	dir := o.WorkDir
	if c.Dir != "" {
		if filepath.IsAbs(c.Dir) {
			dir = c.Dir
		} else {
			dir = filepath.Join(o.WorkDir, c.Dir)
		}
	}

	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	var cmd *exec.Cmd
	if c.Shell != "" {
		if runtime.GOOS == "windows" {
			cmd = exec.CommandContext(ctx, "cmd", "/C", c.Shell)
		} else {
			cmd = exec.CommandContext(ctx, "/bin/sh", "-c", c.Shell)
		}
	} else {
		if len(c.Cmd) == 0 {
			return Result{Command: c.String(), ExitCode: -1, Err: fmt.Errorf("empty command")}
		}
		cmd = exec.CommandContext(ctx, c.Cmd[0], c.Cmd[1:]...)
	}
	cmd.Dir = dir
	cmd.Env = buildEnv(o.Env, c.Env)
	setProcessGroup(cmd)
	cmd.Cancel = func() error { return killGroup(cmd) }
	cmd.WaitDelay = 5 * time.Second

	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out

	start := time.Now()
	err := cmd.Run()
	res := Result{
		Command:  c.String(),
		Duration: time.Since(start),
		Output:   strings.TrimRight(out.String(), "\n"),
	}
	if cmd.ProcessState != nil {
		res.ExitCode = cmd.ProcessState.ExitCode()
	}
	switch {
	case ctx.Err() == context.DeadlineExceeded:
		res.Err = fmt.Errorf("timed out after %s", timeout)
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
	case err != nil:
		res.Err = err
		if res.ExitCode == 0 {
			res.ExitCode = -1
		}
	}
	return res
}

// RunAll executes commands in order and stops at the first failure.
func RunAll(ctx context.Context, cmds []config.Command, o Options) ([]Result, error) {
	var results []Result
	for _, c := range cmds {
		if o.Logf != nil {
			o.Logf("run: %s", c.String())
		}
		r := Run(ctx, c, o)
		results = append(results, r)
		if o.Logf != nil && r.Output != "" {
			o.Logf("%s", indent(r.Output))
		}
		if r.Err != nil {
			return results, fmt.Errorf("command %q failed (exit %d): %v", c.String(), r.ExitCode, r.Err)
		}
	}
	return results, nil
}

func indent(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = "    " + lines[i]
	}
	return strings.Join(lines, "\n")
}

// passthroughEnv lists the variables a deployment command may inherit from the
// deckhand process. Everything else — including the GitHub token — is dropped.
var passthroughEnv = []string{
	"PATH", "HOME", "USER", "LOGNAME", "SHELL", "LANG", "LC_ALL", "TZ", "TMPDIR",
	"SYSTEMROOT", "SystemRoot", "WINDIR", "COMSPEC", "PATHEXT", "TEMP", "TMP",
	"USERPROFILE", "APPDATA", "LOCALAPPDATA", "ProgramData", "ProgramFiles",
	"XDG_RUNTIME_DIR", "DBUS_SESSION_BUS_ADDRESS", "DOCKER_HOST", "SSH_AUTH_SOCK",
}

func buildEnv(sets ...map[string]string) []string {
	env := map[string]string{}
	for _, key := range passthroughEnv {
		if v, ok := os.LookupEnv(key); ok {
			env[key] = v
		}
	}
	for _, m := range sets {
		for k, v := range m {
			env[k] = os.ExpandEnv(v)
		}
	}
	keys := make([]string, 0, len(env))
	for k := range env {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, k := range keys {
		out = append(out, k+"="+env[k])
	}
	return out
}
