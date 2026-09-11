package deploy

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// gitEnv is the hardened environment every git invocation runs with.
//
// Repository content must never influence how git behaves, so hooks are
// disabled, interactive prompts are off, and exotic transports are refused.
func gitEnv(askpassBinary, token string) []string {
	env := []string{
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_NOSYSTEM=1",
		"GCM_INTERACTIVE=never",
		"HOME=" + os.Getenv("HOME"),
		"PATH=" + os.Getenv("PATH"),
	}
	for _, k := range []string{"SYSTEMROOT", "SystemRoot", "USERPROFILE", "APPDATA",
		"LOCALAPPDATA", "TEMP", "TMP", "TMPDIR", "ProgramFiles", "ProgramData",
		"HTTPS_PROXY", "HTTP_PROXY", "NO_PROXY", "https_proxy", "http_proxy", "no_proxy",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO"} {
		if v, ok := os.LookupEnv(k); ok {
			env = append(env, k+"="+v)
		}
	}
	if token != "" && askpassBinary != "" {
		// The token is handed to git through the environment of the child
		// process rather than the command line, so it does not show up in the
		// process list of other users.
		env = append(env,
			"GIT_ASKPASS="+askpassBinary,
			"DECKHAND_ASKPASS=1",
			"DECKHAND_ASKPASS_TOKEN="+token,
		)
	}
	return env
}

// gitFlags are prepended to every git invocation. Repository content must not
// be able to change how git behaves, so hooks are disabled and exotic
// transports are refused.
func (g gitRunner) gitFlags() []string {
	flags := []string{
		"-c", "core.hooksPath=",
		"-c", "protocol.ext.allow=never",
		"-c", "core.fsmonitor=false",
		"-c", "credential.helper=",
		"-c", "advice.detachedHead=false",
	}
	if g.allowLocal {
		// The operator deliberately configured a local or file:// source.
		flags = append(flags, "-c", "protocol.file.allow=always")
	} else {
		flags = append(flags, "-c", "protocol.file.allow=never")
	}
	return flags
}

// gitRunner executes git with the hardened flags and environment.
type gitRunner struct {
	askpass string
	token   string
	// allowLocal permits cloning from a local path, which is only sensible
	// when the configured clone URL is one.
	allowLocal bool
}

func (g gitRunner) run(ctx context.Context, dir string, args ...string) (string, error) {
	full := append(g.gitFlags(), args...)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = dir
	cmd.Env = gitEnv(g.askpass, g.token)
	var out, errb bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errb
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(errb.String())
		if msg == "" {
			msg = err.Error()
		}
		return out.String(), fmt.Errorf("git %s: %s", strings.Join(args, " "), redact(msg, g.token))
	}
	return out.String(), nil
}

func redact(s, token string) string {
	if token == "" {
		return s
	}
	return strings.ReplaceAll(s, token, "[redacted]")
}

// ensureMirror creates or updates the bare mirror clone used as local cache.
func (g gitRunner) ensureMirror(ctx context.Context, mirrorDir, cloneURL string) error {
	if _, err := os.Stat(filepath.Join(mirrorDir, "HEAD")); err != nil {
		if err := os.MkdirAll(filepath.Dir(mirrorDir), 0o750); err != nil {
			return err
		}
		if _, err := g.run(ctx, filepath.Dir(mirrorDir), "clone", "--bare", "--quiet",
			cloneURL, filepath.Base(mirrorDir)); err != nil {
			return err
		}
	}
	_, err := g.run(ctx, mirrorDir, "fetch", "--quiet", "--prune", "--prune-tags", "--tags",
		"origin", "+refs/heads/*:refs/heads/*")
	return err
}

// hasCommit reports whether the mirror already contains a commit.
func (g gitRunner) hasCommit(ctx context.Context, mirrorDir, sha string) bool {
	_, err := g.run(ctx, mirrorDir, "cat-file", "-e", sha+"^{commit}")
	return err == nil
}

// commitTime returns the author time of a commit, for the release metadata.
func (g gitRunner) commitTime(ctx context.Context, mirrorDir, sha string) time.Time {
	out, err := g.run(ctx, mirrorDir, "show", "-s", "--format=%cI", sha)
	if err != nil {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, strings.TrimSpace(out))
	if err != nil {
		return time.Time{}
	}
	return t
}

// commitIdentities returns the author and committer email of a commit.
func (g gitRunner) commitIdentities(ctx context.Context, mirrorDir, sha string) (author, committer string, err error) {
	out, err := g.run(ctx, mirrorDir, "show", "-s", "--format=%ae%n%ce", sha)
	if err != nil {
		return "", "", err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) < 2 {
		return "", "", fmt.Errorf("could not read the author of %s", sha)
	}
	return strings.TrimSpace(lines[0]), strings.TrimSpace(lines[1]), nil
}

// verifySignature checks a signed tag or commit against an allowed-signers file.
func (g gitRunner) verifySignature(ctx context.Context, mirrorDir, ref string, tag bool, allowedSigners string) error {
	args := []string{}
	if allowedSigners != "" {
		args = append(args, "-c", "gpg.format=ssh", "-c", "gpg.ssh.allowedSignersFile="+allowedSigners)
	}
	if tag {
		args = append(args, "verify-tag", ref)
	} else {
		args = append(args, "verify-commit", ref)
	}
	_, err := g.run(ctx, mirrorDir, args...)
	return err
}

// exportTree writes the tree of sha into destDir using `git archive`, extracting
// the stream in-process so no external tar is required and every path can be
// validated before it touches the filesystem.
func (g gitRunner) exportTree(ctx context.Context, mirrorDir, sha, destDir string, overwrite bool) (int, error) {
	full := append(g.gitFlags(), "archive", "--format=tar", sha)
	cmd := exec.CommandContext(ctx, "git", full...)
	cmd.Dir = mirrorDir
	cmd.Env = gitEnv(g.askpass, g.token)
	var errb bytes.Buffer
	cmd.Stderr = &errb
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return 0, err
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	files, extractErr := extractTar(stdout, destDir, overwrite)
	_, _ = io.Copy(io.Discard, stdout)
	waitErr := cmd.Wait()
	if extractErr != nil {
		return files, extractErr
	}
	if waitErr != nil {
		return files, fmt.Errorf("git archive: %s", strings.TrimSpace(errb.String()))
	}
	return files, nil
}

// extractTar unpacks a tar stream, refusing anything that would write outside
// destDir: absolute paths, "..", hard/symlinks pointing out of the tree, and
// writes through a symlinked parent directory.
func extractTar(r io.Reader, destDir string, overwrite bool) (int, error) {
	root, err := filepath.Abs(destDir)
	if err != nil {
		return 0, err
	}
	tr := tar.NewReader(r)
	count := 0
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return count, nil
		}
		if err != nil {
			return count, fmt.Errorf("reading archive: %w", err)
		}
		target, err := safeJoin(root, hdr.Name)
		if err != nil {
			return count, err
		}
		// safeJoin already guarantees this. Restating it here keeps the
		// guarantee next to the writes it protects, where both a reader and a
		// static analyser can see it without following a call.
		if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
			return count, fmt.Errorf("refusing archive entry outside the release directory: %q", hdr.Name)
		}
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := checkTargetInsideRoot(root, target); err != nil {
				return count, err
			}
			if err := os.MkdirAll(target, 0o755); err != nil {
				return count, err
			}
		case tar.TypeReg:
			if err := checkTargetInsideRoot(root, filepath.Dir(target)); err != nil {
				return count, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			if err := checkTargetInsideRoot(root, target); err != nil {
				return count, err
			}
			mode := os.FileMode(hdr.Mode).Perm()
			if mode == 0 {
				mode = 0o644
			}
			flags := os.O_CREATE | os.O_TRUNC | os.O_WRONLY | os.O_EXCL
			if overwrite {
				if info, err := os.Lstat(target); err == nil && !info.IsDir() {
					if err := os.Remove(target); err != nil {
						return count, err
					}
				}
			}
			f, err := os.OpenFile(target, flags, mode)
			if err != nil {
				return count, err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return count, err
			}
			f.Close()
			count++
		case tar.TypeSymlink:
			linkTarget := hdr.Linkname
			resolved := linkTarget
			if !filepath.IsAbs(resolved) {
				resolved = filepath.Join(filepath.Dir(target), linkTarget)
			}
			if !within(root, resolved) {
				return count, fmt.Errorf("refusing symlink %q -> %q: points outside the release directory",
					hdr.Name, linkTarget)
			}
			if err := checkTargetInsideRoot(root, filepath.Dir(target)); err != nil {
				return count, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			if err := checkTargetInsideRoot(root, target); err != nil {
				return count, err
			}
			if overwrite {
				_ = os.Remove(target)
			}
			if err := os.Symlink(linkTarget, target); err != nil {
				return count, fmt.Errorf("symlink %q: %w", hdr.Name, err)
			}
			count++
		case tar.TypeLink:
			source, err := safeJoin(root, hdr.Linkname)
			if err != nil {
				return count, err
			}
			if err := checkTargetInsideRoot(root, source); err != nil {
				return count, err
			}
			if err := checkTargetInsideRoot(root, filepath.Dir(target)); err != nil {
				return count, err
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return count, err
			}
			if err := checkTargetInsideRoot(root, target); err != nil {
				return count, err
			}
			if overwrite {
				_ = os.Remove(target)
			}
			if err := os.Link(source, target); err != nil {
				return count, fmt.Errorf("hardlink %q: %w", hdr.Name, err)
			}
			count++
		default:
			// Devices, fifos and sockets never legitimately appear in a git
			// archive; skipping them is safer than reproducing them.
			continue
		}
	}
}

// safeJoin turns an archive entry name into a path inside root, refusing
// anything that could land elsewhere.
//
// The checks are deliberately explicit and repetitive rather than delegated to
// a helper: each one is a separate, obvious barrier, which keeps the guarantee
// readable and lets static analysis see it too.
func safeJoin(root, name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("refusing archive entry with an empty name")
	}
	// Absolute in any notation: a unix path, a windows drive letter, a
	// drive-relative path such as "\\dir", or a UNC path.
	if filepath.IsAbs(name) || strings.HasPrefix(name, "/") || strings.HasPrefix(name, `\`) {
		return "", fmt.Errorf("refusing archive entry with absolute path %q", name)
	}

	// Reject ".." as a path segment, in either separator style. Matching whole
	// segments rather than the substring ".." keeps legitimate names such as
	// "test..data.txt" working.
	for _, sep := range []string{"/", `\`} {
		for _, segment := range strings.Split(name, sep) {
			if segment == ".." {
				return "", fmt.Errorf("refusing archive entry containing \"..\": %q", name)
			}
		}
	}

	clean := filepath.Clean(filepath.FromSlash(name))
	if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing archive entry escaping the release directory: %q", name)
	}

	target := filepath.Join(root, clean)

	// Belt and braces: the joined path must still start at root.
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("refusing archive entry outside the release directory: %q", name)
	}
	if !within(root, target) {
		return "", fmt.Errorf("refusing archive entry outside the release directory: %q", name)
	}
	return target, nil
}

func within(root, path string) bool {
	rel, err := filepath.Rel(root, filepath.Clean(path))
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(os.PathSeparator))
}

// checkTargetInsideRoot makes sure that writing to target really lands inside
// root, following any symlinks that already exist along the way.
//
// safeJoin alone is not enough: it works on the path as written in the archive,
// while this resolves what is actually on disk. With the in-place strategy the
// target directory is not empty, so an operator's own symlink - or one from an
// earlier revision - could otherwise be used to write through and land
// somewhere else entirely.
func checkTargetInsideRoot(root, target string) error {
	realRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		realRoot = root
	}
	// Find the longest part of the path that already exists; anything beyond
	// it cannot redirect the write.
	existing := target
	for {
		if _, err := os.Lstat(existing); err == nil {
			break
		}
		parent := filepath.Dir(existing)
		if parent == existing {
			return nil
		}
		existing = parent
	}
	resolved, err := filepath.EvalSymlinks(existing)
	if err != nil {
		// A dangling symlink resolves to nothing; refuse rather than guess.
		if info, lerr := os.Lstat(existing); lerr == nil && info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("refusing to write through dangling symlink %q", existing)
		}
		return nil
	}
	if !within(realRoot, resolved) {
		return fmt.Errorf("refusing to write outside the release directory: %q leads to %q",
			target, resolved)
	}
	return nil
}
