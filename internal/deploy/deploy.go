// Package deploy fetches the wanted revision and puts it in place.
//
// With the default "releases" strategy each revision is exported into
// releases/<sha> and a "current" symlink is switched over once the tree is
// complete. Nothing ever runs against a half-written directory, and rolling
// back is just pointing the link at the previous release again.
package deploy

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/gh"
)

// Deployer prepares and activates revisions for one watch.
type Deployer struct {
	Watch    *config.Watch
	StateDir string
	Token    string
	// Askpass is the path to the deckhand binary, used as GIT_ASKPASS helper.
	Askpass string
	Logf    func(format string, args ...interface{})

	git gitRunner
}

// New returns a Deployer for a watch.
func New(w *config.Watch, stateDir, token, askpass string, logf func(string, ...interface{})) *Deployer {
	if logf == nil {
		logf = func(string, ...interface{}) {}
	}
	return &Deployer{
		Watch: w, StateDir: stateDir, Token: token, Askpass: askpass, Logf: logf,
		git: gitRunner{askpass: askpass, token: token, allowLocal: isLocalSource(w.CloneURL)},
	}
}

func (d *Deployer) mirrorDir() string   { return filepath.Join(d.StateDir, "repo.git") }
func (d *Deployer) releasesDir() string { return filepath.Join(d.Watch.Path, "releases") }
func (d *Deployer) sharedDir() string   { return filepath.Join(d.Watch.Path, "shared") }
func (d *Deployer) currentLink() string { return filepath.Join(d.Watch.Path, "current") }

// WorkDir is the directory the run commands are executed in.
func (d *Deployer) WorkDir() string {
	if d.Watch.Strategy == config.StrategyInplace {
		return d.Watch.Path
	}
	return d.currentLink()
}

// isLocalSource reports whether the clone URL points at the local filesystem
// rather than at a remote host.
func isLocalSource(url string) bool {
	if strings.HasPrefix(url, "file://") {
		return true
	}
	return !strings.Contains(url, "://") && !strings.Contains(url, "@")
}

// cloneURL is where the code is fetched from. The token is never embedded in
// the URL; git asks for it through the askpass helper instead.
func (d *Deployer) cloneURL() string { return d.Watch.CloneURL }

// releaseMeta is written into every release directory for humans and scripts.
type releaseMeta struct {
	Watch      string    `json:"watch"`
	Repo       string    `json:"repo"`
	SHA        string    `json:"sha"`
	Ref        string    `json:"ref"`
	Kind       string    `json:"kind"`
	Name       string    `json:"name"`
	DeployedAt time.Time `json:"deployed_at"`
	CommitTime time.Time `json:"commit_time,omitempty"`
}

// Fetch updates the local mirror so the wanted commit is available offline.
func (d *Deployer) Fetch(ctx context.Context) error {
	if err := os.MkdirAll(d.StateDir, 0o750); err != nil {
		return err
	}
	return d.git.ensureMirror(ctx, d.mirrorDir(), d.cloneURL())
}

// Verify applies the optional supply-chain checks configured for the watch.
func (d *Deployer) Verify(ctx context.Context, t *gh.Target) error {
	v := d.Watch.Verify
	if v.PinSHA != "" && !strings.EqualFold(v.PinSHA, t.SHA) {
		return fmt.Errorf("pin_sha mismatch: config pins %s but the trigger resolved to %s", v.PinSHA, t.SHA)
	}
	if v.RequireSignedTag {
		if t.Kind == "branch" {
			return fmt.Errorf("require_signed_tag is set but the trigger watches a branch")
		}
		if err := d.git.verifySignature(ctx, d.mirrorDir(), t.Ref, true, v.AllowedSigners); err != nil {
			return fmt.Errorf("tag signature check failed: %w", err)
		}
		d.Logf("signature ok: tag %s", t.Ref)
	}
	if len(v.AllowedAuthors) > 0 {
		author, committer, err := d.git.commitIdentities(ctx, d.mirrorDir(), t.SHA)
		if err != nil {
			return fmt.Errorf("allowed_authors: %w", err)
		}
		if !anyAllowed(v.AllowedAuthors, author, committer) {
			return fmt.Errorf("revision %s was authored by %s and committed by %s, "+
				"neither of which is in allowed_authors", short(t.SHA), author, committer)
		}
		d.Logf("author ok: %s", author)
	}
	if v.RequireSignedCommit {
		if err := d.git.verifySignature(ctx, d.mirrorDir(), t.SHA, false, v.AllowedSigners); err != nil {
			return fmt.Errorf("commit signature check failed: %w", err)
		}
		d.Logf("signature ok: commit %s", short(t.SHA))
	}
	return nil
}

// Prepare materialises the revision on disk and returns the directory the run
// commands should execute in. Nothing is activated yet.
func (d *Deployer) Prepare(ctx context.Context, t *gh.Target) (string, error) {
	if !d.git.hasCommit(ctx, d.mirrorDir(), t.SHA) {
		return "", fmt.Errorf("commit %s is not in the mirror after fetching", short(t.SHA))
	}
	if d.Watch.Strategy == config.StrategyInplace {
		if err := os.MkdirAll(d.Watch.Path, 0o755); err != nil {
			return "", err
		}
		n, err := d.git.exportTree(ctx, d.mirrorDir(), t.SHA, d.Watch.Path, true)
		if err != nil {
			return "", err
		}
		d.Logf("updated %d files in %s", n, d.Watch.Path)
		if err := d.writeMeta(d.Watch.Path, t, ctx); err != nil {
			return "", err
		}
		return d.Watch.Path, nil
	}

	dir := filepath.Join(d.releasesDir(), t.SHA)
	staging := dir + ".incomplete"
	if err := os.RemoveAll(staging); err != nil {
		return "", err
	}
	if err := os.MkdirAll(staging, 0o755); err != nil {
		return "", err
	}
	n, err := d.git.exportTree(ctx, d.mirrorDir(), t.SHA, staging, false)
	if err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if err := d.linkShared(staging); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if err := d.writeMeta(staging, t, ctx); err != nil {
		_ = os.RemoveAll(staging)
		return "", err
	}
	if err := os.RemoveAll(dir); err != nil {
		return "", err
	}
	if err := os.Rename(staging, dir); err != nil {
		return "", err
	}
	d.Logf("prepared release %s (%d files)", short(t.SHA), n)
	return dir, nil
}

func (d *Deployer) writeMeta(dir string, t *gh.Target, ctx context.Context) error {
	meta := releaseMeta{
		Watch: d.Watch.Name, Repo: d.Watch.Repo, SHA: t.SHA, Ref: t.Ref,
		Kind: t.Kind, Name: t.Name, DeployedAt: time.Now().UTC(),
		CommitTime: d.git.commitTime(ctx, d.mirrorDir(), t.SHA),
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, ".deckhand-release.json"), append(data, '\n'), 0o644)
}

// linkShared wires the configured shared paths into a freshly exported tree so
// configuration and data survive every deployment.
func (d *Deployer) linkShared(releaseDir string) error {
	for _, entry := range d.Watch.Shared {
		clean := strings.TrimSpace(entry)
		isDir := strings.HasSuffix(clean, "/")
		clean = strings.Trim(filepath.ToSlash(filepath.Clean(clean)), "/")
		if clean == "" || clean == "." || strings.HasPrefix(clean, "..") {
			return fmt.Errorf("invalid shared path %q", entry)
		}
		src := filepath.Join(d.sharedDir(), filepath.FromSlash(clean))
		dst := filepath.Join(releaseDir, filepath.FromSlash(clean))

		if _, err := os.Lstat(src); os.IsNotExist(err) {
			// Seed the shared copy from the repository the first time, so a
			// checked-in example config becomes the starting point.
			if info, err := os.Lstat(dst); err == nil {
				if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil {
					return err
				}
				if err := os.Rename(dst, src); err != nil {
					return fmt.Errorf("seeding shared path %q: %w", entry, err)
				}
				isDir = info.IsDir()
			} else if isDir {
				if err := os.MkdirAll(src, 0o750); err != nil {
					return err
				}
			} else {
				if err := os.MkdirAll(filepath.Dir(src), 0o750); err != nil {
					return err
				}
				f, err := os.OpenFile(src, os.O_CREATE|os.O_WRONLY, 0o640)
				if err != nil {
					return err
				}
				f.Close()
			}
		} else if err == nil {
			if info, err := os.Stat(src); err == nil {
				isDir = info.IsDir()
			}
		}
		if err := linkInto(dst, src, isDir); err != nil {
			return fmt.Errorf("linking shared path %q: %w", entry, err)
		}
	}
	return nil
}

// Activate switches the current link to the given release directory. It is a
// no-op for the in-place strategy, where the tree is already live.
func (d *Deployer) Activate(releaseDir string) error {
	if d.Watch.Strategy == config.StrategyInplace {
		return nil
	}
	return replaceLink(d.currentLink(), releaseDir, true)
}

// RollbackTo reactivates a named revision. The caller decides which one: after
// a failed deployment that is the revision that was running before it, while
// "deckhand rollback" means the one before that.
func (d *Deployer) RollbackTo(ctx context.Context, releaseDir, sha string) error {
	if d.Watch.Strategy == config.StrategyInplace {
		if sha == "" {
			return fmt.Errorf("no revision recorded to roll back to")
		}
		_, err := d.git.exportTree(ctx, d.mirrorDir(), sha, d.Watch.Path, true)
		return err
	}
	if releaseDir == "" {
		return fmt.Errorf("no release recorded to roll back to")
	}
	if _, err := os.Stat(releaseDir); err != nil {
		return fmt.Errorf("release %s is gone: %w", releaseDir, err)
	}
	return replaceLink(d.currentLink(), releaseDir, true)
}

// Prune deletes old release directories, keeping the newest keep entries plus
// whatever is currently or previously active.
func (d *Deployer) Prune(keep int, protect ...string) {
	if d.Watch.Strategy == config.StrategyInplace || keep <= 0 {
		return
	}
	entries, err := os.ReadDir(d.releasesDir())
	if err != nil {
		return
	}
	type rel struct {
		path string
		mod  time.Time
	}
	var rels []rel
	keepSet := map[string]bool{}
	for _, p := range protect {
		if p != "" {
			keepSet[filepath.Clean(p)] = true
		}
	}
	for _, e := range entries {
		if !e.IsDir() || strings.HasSuffix(e.Name(), ".incomplete") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		rels = append(rels, rel{filepath.Join(d.releasesDir(), e.Name()), info.ModTime()})
	}
	sort.Slice(rels, func(i, j int) bool { return rels[i].mod.After(rels[j].mod) })
	for i, r := range rels {
		if i < keep || keepSet[filepath.Clean(r.path)] {
			continue
		}
		if err := os.RemoveAll(r.path); err == nil {
			d.Logf("pruned old release %s", filepath.Base(r.path))
		}
	}
}

// anyAllowed reports whether the author or committer is on the list. The
// comparison is case-insensitive because git addresses often are.
//
// Worth knowing: an author line is metadata anyone can set. This guards
// against accident, not against an attacker - for that, require a signature.
func anyAllowed(allowed []string, identities ...string) bool {
	for _, want := range allowed {
		for _, got := range identities {
			if got != "" && strings.EqualFold(strings.TrimSpace(want), got) {
				return true
			}
		}
	}
	return false
}

func short(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// Short exposes the abbreviated SHA helper.
func Short(sha string) string { return short(sha) }

// ProbeLink creates and removes a link, so "deckhand doctor" can tell whether
// the atomic switch will work on this filesystem before a deployment needs it.
func ProbeLink(linkPath, target string) error {
	if err := replaceLink(linkPath, target, true); err != nil {
		return err
	}
	return os.Remove(linkPath)
}
