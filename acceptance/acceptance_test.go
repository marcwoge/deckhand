// Package acceptance drives the built binary through a real deployment.
//
// It exists for the parts that differ per operating system and that unit tests
// therefore cannot reach: the "current" link (a symlink, or a junction on
// Windows without Developer Mode), the shared-path fallbacks, killing a command
// and its children on timeout, and the in-place strategy that exists for when
// links are unavailable at all. Issue #10 is the list this works through.
//
// It is skipped unless DECKHAND_ACCEPTANCE=1, because it clones from GitHub and
// takes rather longer than a unit test:
//
//	DECKHAND_ACCEPTANCE=1 go test ./acceptance/ -v
package acceptance

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

// repo is cloned by every case: public, small, and it is this project, so a
// failure is about Deckhand rather than about someone else's repository.
const repo = "marcwoge/deckhand"

// authLine decides whether the cases authenticate.
//
// Anonymous access is 60 requests an hour per IP, and hosted runners share
// theirs - which is how a run on macOS failed with the limit exhausted while
// nothing was wrong. With a token in the environment the watches use it, which
// is also the path users actually run.
func authLine() string {
	if os.Getenv("DECKHAND_GITHUB_TOKEN") != "" {
		return ""
	}
	return "    auth: none"
}

func TestMain(m *testing.M) {
	if os.Getenv("DECKHAND_ACCEPTANCE") != "1" {
		// Nothing to report; the unit tests cover everything else.
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// binary returns the deckhand binary to exercise, building one if needed.
func binary(t *testing.T) string {
	t.Helper()
	if path := os.Getenv("DECKHAND_BIN"); path != "" {
		abs, err := filepath.Abs(path)
		if err != nil {
			t.Fatal(err)
		}
		return abs
	}
	name := "deckhand"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	path := filepath.Join(t.TempDir(), name)
	build := exec.Command("go", "build", "-o", path, "../cmd/deckhand")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("building the binary: %v\n%s", err, out)
	}
	return path
}

// deckhand runs one command and returns its combined output.
func deckhand(t *testing.T, bin, config string, args ...string) (string, error) {
	t.Helper()
	cmd := exec.Command(bin, append(args, "--config", config)...)
	// A state directory of its own, so cases cannot see each other's history.
	cmd.Env = append(os.Environ(), "DECKHAND_STATE_DIR="+filepath.Join(filepath.Dir(config), "state"))
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// writeConfig puts a configuration next to its deployment directory.
func writeConfig(t *testing.T, body string) (config, path string) {
	t.Helper()
	dir := t.TempDir()
	path = filepath.Join(dir, "srv")
	if err := os.MkdirAll(path, 0o750); err != nil {
		t.Fatal(err)
	}
	config = filepath.Join(dir, "deckhand.yaml")
	body = strings.ReplaceAll(body, "%PATH%", filepath.ToSlash(path))
	body = strings.ReplaceAll(body, "%REPO%", repo)
	body = strings.ReplaceAll(body, "%AUTH%", authLine())
	if err := os.WriteFile(config, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return config, path
}

// recordSHA is a run command that writes the revision it was given into the
// shared directory. It goes through DECKHAND_SHARED_DIR rather than an absolute
// path so that no Windows path has to survive YAML quoting and cmd's
// redirection, and it proves two things at once: the command ran, and the
// environment reached it.
func recordSHA() string {
	if runtime.GOOS == "windows" {
		// PowerShell rather than cmd, whose parsing of the quotes Go puts
		// around an argument does not survive the trip. And Join-Path rather
		// than a backslash, because this string goes through YAML on the way:
		// "...\ran.txt" in a double-quoted scalar is a carriage return
		// followed by "an.txt", which Windows reports as an illegal path.
		return `["powershell", "-NoProfile", "-Command",` +
			` "[IO.File]::WriteAllText((Join-Path $env:DECKHAND_SHARED_DIR 'ran.txt'), $env:DECKHAND_SHA)"]`
	}
	return `["sh", "-c", "printf %s \"$DECKHAND_SHA\" > \"$DECKHAND_SHARED_DIR/ran.txt\""]`
}

// nothing is a command that succeeds on every platform.
func nothing() string {
	if runtime.GOOS == "windows" {
		return `["cmd", "/c", "exit", "0"]`
	}
	return `["true"]`
}

// isRevision rejects what a failed environment expansion looks like - the
// literal variable name, or an empty file - rather than only checking a length.
func isRevision(s string) bool {
	s = strings.TrimSpace(s)
	if len(s) != 40 {
		return false
	}
	for _, r := range s {
		if !strings.ContainsRune("0123456789abcdefABCDEF", r) {
			return false
		}
	}
	return true
}

// The default strategy: a release directory per revision, with "current"
// pointing at it. On Windows that link is a symlink with Developer Mode on and a
// junction without, which is the single most platform-dependent thing Deckhand
// does.
func TestReleaseStrategyAndCurrentLink(t *testing.T) {
	bin := binary(t)
	config, path := writeConfig(t, `
version: 1
defaults:
  command_timeout: 2m
watch:
  - name: app
    repo: %REPO%
%AUTH%
    trigger: { type: release }
    path: %PATH%
    shared:
      - .env
    run:
      - `+recordSHA()+`
`)

	if out, err := deckhand(t, bin, config, "check"); err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	out, err := deckhand(t, bin, config, "deploy", "app", "--force")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}

	current := filepath.Join(path, "current")
	resolved, err := filepath.EvalSymlinks(current)
	if err != nil {
		t.Fatalf("current does not resolve: %v\n%s", err, out)
	}
	if !strings.Contains(filepath.ToSlash(resolved), "/releases/") {
		t.Errorf("current resolves to %s, want a release directory", resolved)
	}
	// The exported tree has to actually be there.
	if _, err := os.Stat(filepath.Join(current, "go.mod")); err != nil {
		t.Errorf("the deployed tree is missing go.mod: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "shared")); err != nil {
		t.Errorf("no shared directory: %v", err)
	}

	ran, err := os.ReadFile(filepath.Join(path, "shared", "ran.txt"))
	if err != nil {
		t.Fatalf("the run command did not run: %v\n%s", err, out)
	}
	if !isRevision(string(ran)) {
		t.Errorf("the run command saw DECKHAND_SHA as %q, want a revision", ran)
	}

	status, err := deckhand(t, bin, config, "status")
	if err != nil {
		t.Fatalf("status: %v\n%s", err, status)
	}
	if !strings.Contains(status, "app") {
		t.Errorf("status does not mention the watch:\n%s", status)
	}
}

// The escape hatch for when links are unavailable: the revision is written
// straight into the path, with no releases directory at all.
func TestInplaceStrategy(t *testing.T) {
	bin := binary(t)
	config, path := writeConfig(t, `
version: 1
watch:
  - name: app
    repo: %REPO%
%AUTH%
    strategy: inplace
    trigger: { type: release }
    path: %PATH%
    run:
      - `+nothing()+`
`)

	if out, err := deckhand(t, bin, config, "deploy", "app", "--force"); err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(path, "go.mod")); err != nil {
		t.Errorf("inplace did not write into the path itself: %v", err)
	}
	if _, err := os.Stat(filepath.Join(path, "releases")); err == nil {
		t.Error("inplace must not create a releases directory")
	}
}

// A command that outlives its timeout has to be killed, together with whatever
// it started - taskkill /T on Windows, a process group elsewhere. Without this
// a hung deploy step holds the watch forever.
func TestCommandTimeoutKillsTheCommand(t *testing.T) {
	bin := binary(t)
	sleep := `["sh", "-c", "sleep 120"]`
	if runtime.GOOS == "windows" {
		sleep = `["powershell", "-NoProfile", "-Command", "Start-Sleep -Seconds 120"]`
	}
	config, _ := writeConfig(t, `
version: 1
defaults:
  command_timeout: 5s
watch:
  - name: app
    repo: %REPO%
%AUTH%
    trigger: { type: release }
    path: %PATH%
    run:
      - `+sleep+`
`)

	start := time.Now()
	out, err := deckhand(t, bin, config, "deploy", "app", "--force")
	elapsed := time.Since(start)
	if err == nil {
		t.Fatalf("a command that never finishes must fail the deployment:\n%s", out)
	}
	// Generous: the clone has to happen first. The point is that it does not
	// sit there for two minutes.
	if elapsed > 90*time.Second {
		t.Errorf("the deployment took %s; the timeout did not kill the command", elapsed)
	}
	if !strings.Contains(strings.ToLower(out), "timed out") {
		t.Errorf("output does not mention the timeout:\n%s", out)
	}
}

// A health check that cannot pass must leave the state honest rather than
// claiming a working deployment.
func TestFailingHealthCheckIsReported(t *testing.T) {
	bin := binary(t)
	config, _ := writeConfig(t, `
version: 1
watch:
  - name: app
    repo: %REPO%
%AUTH%
    trigger: { type: release }
    path: %PATH%
    run:
      - `+nothing()+`
    health:
      http: http://127.0.0.1:1/healthz
      retries: 2
      interval: 1s
      initial_delay: 0s
`)

	out, err := deckhand(t, bin, config, "deploy", "app", "--force")
	if err == nil {
		t.Fatalf("a failing health check must fail the deployment:\n%s", out)
	}
	if !strings.Contains(strings.ToLower(out), "health check failed") {
		t.Errorf("output does not name the health check:\n%s", out)
	}
	status, _ := deckhand(t, bin, config, "status")
	if !strings.Contains(status, "UNVERIFIED") && !strings.Contains(status, "failure") {
		t.Errorf("status hides that the deployment did not pass its checks:\n%s", status)
	}
}

// Where the platform puts state and logs, which is the other thing that differs
// per operating system and that nothing else checks on a real machine.
func TestPlatformPaths(t *testing.T) {
	bin := binary(t)
	config, _ := writeConfig(t, `
version: 1
watch:
  - name: app
    repo: %REPO%
%AUTH%
    trigger: { type: release }
    path: %PATH%
    run:
      - `+nothing()+`
`)
	// Without DECKHAND_STATE_DIR the default location is used, which is what
	// this case is about.
	cmd := exec.Command(bin, "check", "--config", config)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("check: %v\n%s", err, out)
	}
	text := string(out)
	if !strings.Contains(text, "state directory:") {
		t.Fatalf("check does not report the state directory:\n%s", text)
	}
	t.Logf("%s: %s", runtime.GOOS, firstLineContaining(text, "state directory:"))
}

func firstLineContaining(text, want string) string {
	for _, line := range strings.Split(text, "\n") {
		if strings.Contains(line, want) {
			return strings.TrimSpace(line)
		}
	}
	return ""
}
