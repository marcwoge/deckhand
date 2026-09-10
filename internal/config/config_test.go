package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func write(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "deckhand.yaml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

const minimal = `
version: 1
watch:
  - name: app
    repo: acme/app
    trigger:
      type: release
    path: /tmp/deckhand-test-app
    run:
      - ["echo", "hello"]
`

func TestLoadMinimal(t *testing.T) {
	cfg, err := Load(write(t, minimal))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	w := cfg.Watches[0]
	if w.Strategy != StrategyReleases {
		t.Errorf("strategy = %q, want %q", w.Strategy, StrategyReleases)
	}
	if w.Auth != "token" {
		t.Errorf("auth = %q, want token", w.Auth)
	}
	if w.CloneURL != "https://github.com/acme/app.git" {
		t.Errorf("clone url = %q", w.CloneURL)
	}
	if w.Rollback != "auto" {
		t.Errorf("rollback = %q, want auto", w.Rollback)
	}
	if !w.Window.OpenAt(nowUTC()) {
		t.Error("a watch without a window must deploy immediately")
	}
	if w.Owner() != "acme" || w.RepoName() != "app" {
		t.Errorf("repo split wrong: %s / %s", w.Owner(), w.RepoName())
	}
}

func TestRejectsUnknownFields(t *testing.T) {
	_, err := Load(write(t, minimal+"\nunexpected: true\n"))
	if err == nil || !strings.Contains(err.Error(), "unexpected") {
		t.Fatalf("typos must be reported, got %v", err)
	}
}

func TestRejectsWorldWritableConfig(t *testing.T) {
	p := write(t, minimal)
	if err := os.Chmod(p, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(p); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("a world-writable config must be refused, got %v", err)
	}
}

func TestRejectsStringCommand(t *testing.T) {
	body := strings.Replace(minimal, `      - ["echo", "hello"]`, `      - echo hello`, 1)
	_, err := Load(write(t, body))
	if err == nil || !strings.Contains(err.Error(), "shell") {
		t.Fatalf("plain string commands must be refused with guidance, got %v", err)
	}
}

func TestRejectsBadValues(t *testing.T) {
	cases := map[string]string{
		"repo without owner": strings.Replace(minimal, "acme/app", "app", 1),
		"missing trigger":    strings.Replace(minimal, "      type: release", "      type: \"\"", 1),
		"no commands":        strings.Replace(minimal, "    run:\n      - [\"echo\", \"hello\"]", "    run: []", 1),
		"tiny poll":          minimal + "\ndefaults:\n  poll_interval: 1s\n",
	}
	for name, body := range cases {
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s should have been rejected", name)
		}
	}
}

func TestSharedRequiresReleaseStrategy(t *testing.T) {
	body := minimal + "    strategy: inplace\n    shared:\n      - .env\n"
	if _, err := Load(write(t, body)); err == nil {
		t.Fatal("shared paths must require the releases strategy")
	}
}

func TestTokenResolution(t *testing.T) {
	t.Setenv("DECKHAND_TEST_TOKEN", "secret-value")
	g := GitHub{TokenEnv: "DECKHAND_TEST_TOKEN"}
	got, err := g.ResolveToken()
	if err != nil || got != "secret-value" {
		t.Fatalf("token from env = %q, %v", got, err)
	}

	dir := t.TempDir()
	f := filepath.Join(dir, "token")
	if err := os.WriteFile(f, []byte("file-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	g = GitHub{TokenFile: f}
	if got, err := g.ResolveToken(); err != nil || got != "file-token" {
		t.Fatalf("token from file = %q, %v", got, err)
	}
	if err := os.Chmod(f, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := g.ResolveToken(); err == nil {
		t.Fatal("a token file readable by others must be refused")
	}
}
