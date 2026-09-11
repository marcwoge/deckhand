package deploy

import (
	"archive/tar"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func tarball(t *testing.T, entries ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range entries {
		body := ""
		if h.Typeflag == tar.TypeReg {
			body = "content of " + h.Name
			h.Size = int64(len(body))
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if body != "" {
			if _, err := tw.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestExtractNormalArchive(t *testing.T) {
	dir := t.TempDir()
	data := tarball(t,
		&tar.Header{Name: "app/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "app/main.go", Typeflag: tar.TypeReg, Mode: 0o644},
		&tar.Header{Name: "app/run.sh", Typeflag: tar.TypeReg, Mode: 0o755},
	)
	n, err := extractTar(bytes.NewReader(data), dir, false)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 2 {
		t.Errorf("extracted %d files, want 2", n)
	}
	info, err := os.Stat(filepath.Join(dir, "app", "run.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm()&0o100 == 0 {
		t.Error("the executable bit must survive extraction")
	}
}

// A release directory must never be a way to write elsewhere on the machine,
// even if the repository is hostile.
func TestExtractRefusesPathTraversal(t *testing.T) {
	for _, name := range []string{"../escape.txt", "/etc/evil.conf", "a/../../escape.txt"} {
		dir := t.TempDir()
		data := tarball(t, &tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0o644})
		if _, err := extractTar(bytes.NewReader(data), dir, false); err == nil {
			t.Errorf("entry %q must be refused", name)
		}
	}
}

func TestExtractRefusesEscapingSymlink(t *testing.T) {
	dir := t.TempDir()
	data := tarball(t, &tar.Header{Name: "link", Typeflag: tar.TypeSymlink, Linkname: "/etc/passwd"})
	_, err := extractTar(bytes.NewReader(data), dir, false)
	if err == nil || !strings.Contains(err.Error(), "outside") {
		t.Fatalf("symlink out of the tree must be refused, got %v", err)
	}
}

// The classic tar-slip: a symlink to a directory outside the tree, followed by
// a file written "through" it.
func TestExtractRefusesWriteThroughSymlinkedDirectory(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "sneaky")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := tarball(t, &tar.Header{Name: "sneaky/payload", Typeflag: tar.TypeReg, Mode: 0o644})
	if _, err := extractTar(bytes.NewReader(data), dir, false); err == nil {
		t.Fatal("writing through a symlinked directory must be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "payload")); err == nil {
		t.Fatal("the file escaped the release directory")
	}
}

func TestExtractSkipsDeviceNodes(t *testing.T) {
	dir := t.TempDir()
	data := tarball(t,
		&tar.Header{Name: "dev", Typeflag: tar.TypeChar, Mode: 0o666, Devmajor: 1, Devminor: 3},
		&tar.Header{Name: "ok.txt", Typeflag: tar.TypeReg, Mode: 0o644},
	)
	n, err := extractTar(bytes.NewReader(data), dir, false)
	if err != nil {
		t.Fatalf("extract: %v", err)
	}
	if n != 1 {
		t.Errorf("extracted %d entries, want only the regular file", n)
	}
	if _, err := os.Lstat(filepath.Join(dir, "dev")); err == nil {
		t.Error("device nodes must not be recreated")
	}
}

func TestExtractOverwriteMode(t *testing.T) {
	dir := t.TempDir()
	existing := filepath.Join(dir, "app.txt")
	if err := os.WriteFile(existing, []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	data := tarball(t, &tar.Header{Name: "app.txt", Typeflag: tar.TypeReg, Mode: 0o644})
	if _, err := extractTar(bytes.NewReader(data), dir, false); err == nil {
		t.Error("without overwrite an existing file must be an error")
	}
	if _, err := extractTar(bytes.NewReader(data), dir, true); err != nil {
		t.Fatalf("overwrite mode: %v", err)
	}
	body, err := os.ReadFile(existing)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) == "old" {
		t.Error("overwrite mode must replace the file")
	}
}

// With the in-place strategy the target directory is not empty, so a symlink
// the operator (or an earlier revision) put there must not become a way to
// write outside the tree. Directories, symlinks and hard links were previously
// only checked as paths, not as what they resolve to on disk.
func TestExtractRefusesDirectoryThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "data")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := tarball(t, &tar.Header{Name: "data/created", Typeflag: tar.TypeDir, Mode: 0o755})

	if _, err := extractTar(bytes.NewReader(data), dir, true); err == nil {
		t.Fatal("creating a directory through a symlink must be refused")
	}
	if _, err := os.Stat(filepath.Join(outside, "created")); err == nil {
		t.Fatal("a directory was created outside the release directory")
	}
}

func TestExtractRefusesSymlinkThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(dir, "data")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := tarball(t, &tar.Header{Name: "data/link", Typeflag: tar.TypeSymlink, Linkname: "target"})

	if _, err := extractTar(bytes.NewReader(data), dir, true); err == nil {
		t.Fatal("creating a symlink through a symlink must be refused")
	}
	if _, err := os.Lstat(filepath.Join(outside, "link")); err == nil {
		t.Fatal("a symlink was created outside the release directory")
	}
}

func TestExtractRefusesHardLinkThroughSymlink(t *testing.T) {
	dir := t.TempDir()
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "source.txt"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(dir, "data")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := tarball(t, &tar.Header{Name: "data/hard", Typeflag: tar.TypeLink, Linkname: "source.txt"})

	if _, err := extractTar(bytes.NewReader(data), dir, true); err == nil {
		t.Fatal("creating a hard link through a symlink must be refused")
	}
}

// A symlink pointing nowhere must not be silently treated as a safe path.
func TestExtractRefusesDanglingSymlinkParent(t *testing.T) {
	dir := t.TempDir()
	if err := os.Symlink(filepath.Join(dir, "does-not-exist"), filepath.Join(dir, "broken")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := tarball(t, &tar.Header{Name: "broken/file.txt", Typeflag: tar.TypeReg, Mode: 0o644})

	if _, err := extractTar(bytes.NewReader(data), dir, true); err == nil {
		t.Fatal("writing through a dangling symlink must be refused")
	}
}

// A symlink that stays inside the tree is legitimate and must keep working.
func TestExtractAllowsSymlinkInsideTree(t *testing.T) {
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "real"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(dir, "real"), filepath.Join(dir, "alias")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	data := tarball(t, &tar.Header{Name: "alias/file.txt", Typeflag: tar.TypeReg, Mode: 0o644})

	if _, err := extractTar(bytes.NewReader(data), dir, true); err != nil {
		t.Fatalf("a symlink that stays inside the tree must be allowed: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "real", "file.txt")); err != nil {
		t.Errorf("the file did not land in the linked directory: %v", err)
	}
}

// safeJoin is the single barrier between an archive entry and the filesystem,
// so it gets its own table rather than only being covered through extractTar.
func TestSafeJoinRejectsEscapes(t *testing.T) {
	root := "/srv/app/releases/abc"
	bad := []string{
		"../escape.txt",
		"../../escape.txt",
		"a/../../escape.txt",
		"a/b/../../../escape.txt",
		"..",
		"/etc/passwd",
		`\etc\passwd`,
		`\\server\share\file`,
		`a\..\..\escape.txt`,
		"foo/../../bar",
		"",
	}
	for _, name := range bad {
		if got, err := safeJoin(root, name); err == nil {
			t.Errorf("safeJoin(%q) = %q, want an error", name, got)
		}
	}
}

// Names that merely look suspicious must still work, or the tool would reject
// perfectly ordinary repositories.
func TestSafeJoinAcceptsLegitimateNames(t *testing.T) {
	root := "/srv/app/releases/abc"
	good := map[string]string{
		"main.go":              "/srv/app/releases/abc/main.go",
		"cmd/app/main.go":      "/srv/app/releases/abc/cmd/app/main.go",
		"test..data.txt":       "/srv/app/releases/abc/test..data.txt",
		"..hidden":             "/srv/app/releases/abc/..hidden",
		"a..b/c..d.txt":        "/srv/app/releases/abc/a..b/c..d.txt",
		"./relative.txt":       "/srv/app/releases/abc/relative.txt",
		"dir/./file.txt":       "/srv/app/releases/abc/dir/file.txt",
		".github/workflows/ci": "/srv/app/releases/abc/.github/workflows/ci",
	}
	for name, want := range good {
		got, err := safeJoin(root, name)
		if err != nil {
			t.Errorf("safeJoin(%q) refused a legitimate name: %v", name, err)
			continue
		}
		if got != want {
			t.Errorf("safeJoin(%q) = %q, want %q", name, got, want)
		}
	}
}

// An entry naming the root itself is refused: it carries nothing, and allowing
// it would force every path check to carry an exception.
func TestSafeJoinRejectsRootItself(t *testing.T) {
	for _, name := range []string{".", "./", "a/.."} {
		if got, err := safeJoin("/srv/app/releases/abc", name); err == nil {
			t.Errorf("safeJoin(%q) = %q, want an error", name, got)
		}
	}
}
