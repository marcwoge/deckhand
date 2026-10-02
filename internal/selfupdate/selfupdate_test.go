package selfupdate

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// release is a stub of the GitHub release endpoints: the API, the assets and the
// checksum file, with the checksums computed from what it serves.
type release struct {
	tag    string
	assets map[string][]byte
	// noBundle drops the signature, which must stop an update rather than
	// letting an unsigned release through.
	noBundle bool
	srv      *httptest.Server
}

func newRelease(t *testing.T, tag string, binary []byte) *release {
	t.Helper()
	r := &release{tag: tag, assets: map[string][]byte{}}
	r.assets[AssetName(tag)] = binary

	var sums strings.Builder
	for name, body := range r.assets {
		digest := sha256.Sum256(body)
		fmt.Fprintf(&sums, "%s  %s\n", hex.EncodeToString(digest[:]), name)
	}
	r.assets["SHA256SUMS"] = []byte(sums.String())
	r.assets["SHA256SUMS.bundle"] = []byte(`{"mediaType":"stub"}`)

	r.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if strings.HasSuffix(req.URL.Path, "/releases/latest") {
			fmt.Fprintf(w, `{"tag_name":%q}`, r.tag)
			return
		}
		name := req.URL.Path[strings.LastIndexByte(req.URL.Path, '/')+1:]
		if name == "SHA256SUMS.bundle" && r.noBundle {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		body, ok := r.assets[name]
		if !ok {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_, _ = w.Write(body)
	}))
	t.Cleanup(r.srv.Close)
	return r
}

// updater points at the stub, with a destination inside a temporary directory.
func (r *release) updater(t *testing.T, current string) (*Updater, string) {
	t.Helper()
	dir := t.TempDir()
	dest := filepath.Join(dir, "deckhand")
	if err := os.WriteFile(dest, []byte(script("the old one")), 0o755); err != nil {
		t.Fatal(err)
	}
	return &Updater{
		Repo:     "marcwoge/deckhand",
		API:      r.srv.URL,
		Download: r.srv.URL,
		Current:  current,
		Dest:     dest,
		Verify:   func(context.Context, string, string, string) error { return nil },
	}, dest
}

// script is a stand-in for the binary: it has to actually run, because the
// updater refuses to install something that cannot state its version.
func script(what string) string {
	return "#!/bin/sh\necho 'deckhand " + what + "'\n"
}

func TestUpdateInstallsAndKeepsThePrevious(t *testing.T) {
	rel := newRelease(t, "v9.9.9", []byte(script("v9.9.9")))
	u, dest := rel.updater(t, "v0.1.1")

	result, err := u.Update(context.Background(), "v9.9.9")
	if err != nil {
		t.Fatal(err)
	}
	installed, err := os.ReadFile(dest)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(installed), "v9.9.9") {
		t.Errorf("the destination still holds %q", installed)
	}
	if result.Previous == "" {
		t.Fatal("no previous binary was kept; a rollback would need the network")
	}
	previous, err := os.ReadFile(result.Previous)
	if err != nil {
		t.Fatalf("the kept binary is unreadable: %v", err)
	}
	if !strings.Contains(string(previous), "the old one") {
		t.Errorf("the kept binary holds %q", previous)
	}
	if info, err := os.Stat(dest); err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("the installed binary is not executable: %v %v", info, err)
	}

	// And back again, without touching the network.
	if err := u.Rollback(result); err != nil {
		t.Fatal(err)
	}
	back, _ := os.ReadFile(dest)
	if !strings.Contains(string(back), "the old one") {
		t.Errorf("after the rollback the destination holds %q", back)
	}
}

// Checking SHA256SUMS alone would prove nothing, so a failing signature has to
// stop everything - and leave the working binary where it is.
func TestFailedVerificationChangesNothing(t *testing.T) {
	rel := newRelease(t, "v9.9.9", []byte(script("v9.9.9")))
	u, dest := rel.updater(t, "v0.1.1")
	u.Verify = func(context.Context, string, string, string) error {
		return fmt.Errorf("no matching signatures")
	}

	if _, err := u.Update(context.Background(), "v9.9.9"); err == nil {
		t.Fatal("an unverifiable release must not be installed")
	}
	body, _ := os.ReadFile(dest)
	if !strings.Contains(string(body), "the old one") {
		t.Errorf("the destination was touched: %q", body)
	}
}

func TestMissingSignatureIsRefused(t *testing.T) {
	rel := newRelease(t, "v9.9.9", []byte(script("v9.9.9")))
	rel.noBundle = true
	u, dest := rel.updater(t, "v0.1.1")

	_, err := u.Update(context.Background(), "v9.9.9")
	if err == nil {
		t.Fatal("a release without a signature must be refused")
	}
	if !strings.Contains(err.Error(), "not signed") {
		t.Errorf("error = %v, want it to say the release is not signed", err)
	}
	body, _ := os.ReadFile(dest)
	if !strings.Contains(string(body), "the old one") {
		t.Errorf("the destination was touched: %q", body)
	}
}

func TestChecksumMismatchIsRefused(t *testing.T) {
	rel := newRelease(t, "v9.9.9", []byte(script("v9.9.9")))
	// Serve a different binary than the one the checksums describe, which is
	// what a substituted asset looks like.
	rel.assets[AssetName("v9.9.9")] = []byte(script("tampered"))
	u, dest := rel.updater(t, "v0.1.1")

	_, err := u.Update(context.Background(), "v9.9.9")
	if err == nil {
		t.Fatal("a checksum mismatch must not be installed")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("error = %v", err)
	}
	body, _ := os.ReadFile(dest)
	if !strings.Contains(string(body), "the old one") {
		t.Errorf("the destination was touched: %q", body)
	}
}

// Wrong architecture, truncated download, a libc that is too old: whatever the
// reason, a binary that cannot state its version must not replace a working one.
func TestABinaryThatDoesNotRunIsRefused(t *testing.T) {
	rel := newRelease(t, "v9.9.9", []byte("not a program at all"))
	u, dest := rel.updater(t, "v0.1.1")

	if _, err := u.Update(context.Background(), "v9.9.9"); err == nil {
		t.Fatal("a binary that does not run must not be installed")
	}
	body, _ := os.ReadFile(dest)
	if !strings.Contains(string(body), "the old one") {
		t.Errorf("the working binary is gone, leaving %q", body)
	}
}

func TestLatestReadsTheTag(t *testing.T) {
	rel := newRelease(t, "v1.2.3", []byte(script("v1.2.3")))
	u := &Updater{API: rel.srv.URL, Download: rel.srv.URL, Dest: filepath.Join(t.TempDir(), "d")}
	got, err := u.Latest(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != "v1.2.3" {
		t.Errorf("latest = %q", got)
	}
}

// sha256sum writes "*name" for an entry read in binary mode, and some releases
// are checksummed that way.
func TestChecksumForAcceptsBinaryModeEntries(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "SHA256SUMS")
	body := "aaaa  deckhand_v1_linux_amd64\nbbbb *deckhand_v1_darwin_arm64\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	for asset, want := range map[string]string{
		"deckhand_v1_linux_amd64":  "aaaa",
		"deckhand_v1_darwin_arm64": "bbbb",
	} {
		got, err := checksumFor(path, asset)
		if err != nil {
			t.Errorf("%s: %v", asset, err)
			continue
		}
		if got != want {
			t.Errorf("%s = %q, want %q", asset, got, want)
		}
	}
	if _, err := checksumFor(path, "deckhand_v1_windows_amd64.exe"); err == nil {
		t.Error("an asset that is not listed must be an error")
	}
}

// The kept binary's name comes from a version string, which arrives from a
// linker flag and should not be able to produce a path.
func TestVersionSuffixIsUsableInAFilename(t *testing.T) {
	cases := map[string]string{
		"v0.1.1":        "v0.1.1",
		"dev":           "dev",
		"0.1.2~dev":     "0.1.2~dev",
		"":              "previous",
		"../../etc/foo": "....etcfoo",
		"a/b":           "ab",
	}
	for in, want := range cases {
		if got := versionSuffix(in); got != want {
			t.Errorf("versionSuffix(%q) = %q, want %q", in, got, want)
		}
	}
}
