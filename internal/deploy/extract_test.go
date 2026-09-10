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
