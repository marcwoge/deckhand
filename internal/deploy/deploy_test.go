package deploy

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/gh"
)

// makeRepo builds a throwaway git repository and returns its path plus the SHAs
// of two commits.
func makeRepo(t *testing.T) (string, string, string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) string {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
			"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return strings.TrimSpace(string(out))
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "app.txt"), []byte("version one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".env"), []byte("FROM_REPO=1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "first")
	first := run("rev-parse", "HEAD")

	if err := os.WriteFile(filepath.Join(dir, "app.txt"), []byte("version two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "second")
	second := run("rev-parse", "HEAD")
	return dir, first, second
}

func testWatch(t *testing.T, source, path string) *config.Watch {
	t.Helper()
	return &config.Watch{
		Name: "test", Repo: "acme/app", CloneURL: source, Path: path,
		Strategy: config.StrategyReleases, Shared: []string{".env", "data/"},
		KeepReleases: 2,
	}
}

func TestDeployPrepareActivateRollback(t *testing.T) {
	source, first, second := makeRepo(t)
	root := t.TempDir()
	state := filepath.Join(root, "state")
	target := filepath.Join(root, "srv")

	w := testWatch(t, source, target)
	d := New(w, state, "", "", func(string, ...interface{}) {})
	ctx := context.Background()

	if err := d.Fetch(ctx); err != nil {
		t.Fatalf("fetch: %v", err)
	}

	rel1, err := d.Prepare(ctx, &gh.Target{SHA: first, Ref: "main", Kind: "branch"})
	if err != nil {
		t.Fatalf("prepare first: %v", err)
	}
	if err := d.Activate(rel1); err != nil {
		t.Fatalf("activate: %v", err)
	}
	body, err := os.ReadFile(filepath.Join(d.WorkDir(), "app.txt"))
	if err != nil || strings.TrimSpace(string(body)) != "version one" {
		t.Fatalf("current tree wrong: %q, %v", body, err)
	}

	// The shared .env was seeded from the repository and lives outside the
	// release, so operator edits survive the next deployment.
	shared := filepath.Join(target, "shared", ".env")
	if err := os.WriteFile(shared, []byte("EDITED=yes\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(target, "shared", "data"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(target, "shared", "data", "db.sqlite"), []byte("rows"), 0o600); err != nil {
		t.Fatal(err)
	}

	rel2, err := d.Prepare(ctx, &gh.Target{SHA: second, Ref: "main", Kind: "branch"})
	if err != nil {
		t.Fatalf("prepare second: %v", err)
	}
	if err := d.Activate(rel2); err != nil {
		t.Fatalf("activate second: %v", err)
	}
	body, _ = os.ReadFile(filepath.Join(d.WorkDir(), "app.txt"))
	if strings.TrimSpace(string(body)) != "version two" {
		t.Fatalf("switch did not take effect: %q", body)
	}
	body, err = os.ReadFile(filepath.Join(d.WorkDir(), ".env"))
	if err != nil || strings.TrimSpace(string(body)) != "EDITED=yes" {
		t.Fatalf("shared file did not survive the deployment: %q, %v", body, err)
	}
	if _, err := os.Stat(filepath.Join(d.WorkDir(), "data", "db.sqlite")); err != nil {
		t.Fatalf("shared directory did not survive the deployment: %v", err)
	}

	// Release metadata is written for humans and for scripts.
	if _, err := os.Stat(filepath.Join(rel2, ".deckhand-release.json")); err != nil {
		t.Errorf("release metadata missing: %v", err)
	}

	// Rolling back restores the previous tree.
	st := &State{CurrentRelease: rel2, PreviousRelease: rel1, PreviousSHA: first}
	if err := d.Rollback(ctx, st); err != nil {
		t.Fatalf("rollback: %v", err)
	}
	body, _ = os.ReadFile(filepath.Join(d.WorkDir(), "app.txt"))
	if strings.TrimSpace(string(body)) != "version one" {
		t.Fatalf("rollback did not take effect: %q", body)
	}
}

func TestPruneKeepsProtectedReleases(t *testing.T) {
	source, first, second := makeRepo(t)
	root := t.TempDir()
	target := filepath.Join(root, "srv")
	w := testWatch(t, source, target)
	w.KeepReleases = 1
	d := New(w, filepath.Join(root, "state"), "", "", func(string, ...interface{}) {})
	ctx := context.Background()
	if err := d.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	rel1, err := d.Prepare(ctx, &gh.Target{SHA: first, Ref: "main", Kind: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	rel2, err := d.Prepare(ctx, &gh.Target{SHA: second, Ref: "main", Kind: "branch"})
	if err != nil {
		t.Fatal(err)
	}
	d.Prune(1, rel2, rel1)
	for _, r := range []string{rel1, rel2} {
		if _, err := os.Stat(r); err != nil {
			t.Errorf("protected release %s was pruned", filepath.Base(r))
		}
	}
}

func TestInplaceStrategyKeepsForeignFiles(t *testing.T) {
	source, first, second := makeRepo(t)
	root := t.TempDir()
	target := filepath.Join(root, "srv")
	w := &config.Watch{Name: "test", Repo: "acme/app", CloneURL: source, Path: target,
		Strategy: config.StrategyInplace}
	d := New(w, filepath.Join(root, "state"), "", "", func(string, ...interface{}) {})
	ctx := context.Background()
	if err := d.Fetch(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Prepare(ctx, &gh.Target{SHA: first, Ref: "main", Kind: "branch"}); err != nil {
		t.Fatal(err)
	}
	local := filepath.Join(target, "uploads.dat")
	if err := os.WriteFile(local, []byte("user data"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Prepare(ctx, &gh.Target{SHA: second, Ref: "main", Kind: "branch"}); err != nil {
		t.Fatalf("second in-place deploy: %v", err)
	}
	if _, err := os.Stat(local); err != nil {
		t.Error("in-place updates must not delete files that are not in the repository")
	}
	body, _ := os.ReadFile(filepath.Join(target, "app.txt"))
	if strings.TrimSpace(string(body)) != "version two" {
		t.Errorf("in-place update did not replace tracked files: %q", body)
	}
}
