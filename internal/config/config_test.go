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

func TestShortNotifyFormBecomesAChannel(t *testing.T) {
	body := minimal + `
notify:
  on: [failure]
  webhook: https://ntfy.sh/my-topic
  format: ntfy
`
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Notify.Channels) != 1 {
		t.Fatalf("got %d channels, want 1", len(cfg.Notify.Channels))
	}
	ch := cfg.Notify.Channels[0]
	if ch.Type != "ntfy" || ch.URL != "https://ntfy.sh/my-topic" {
		t.Errorf("channel = %+v", ch)
	}
}

func TestNotifyChannelValidation(t *testing.T) {
	cases := map[string]string{
		"telegram without chat_id": "\nnotify:\n  channels:\n    - type: telegram\n      token: abc\n",
		"telegram without token":   "\nnotify:\n  channels:\n    - type: telegram\n      chat_id: \"1\"\n",
		"unknown type":             "\nnotify:\n  channels:\n    - type: carrier-pigeon\n      url: https://x\n",
		"missing url":              "\nnotify:\n  channels:\n    - type: ntfy\n",
		"url without scheme":       "\nnotify:\n  channels:\n    - type: ntfy\n      url: ntfy.sh/topic\n",
		"bad priority":             "\nnotify:\n  channels:\n    - type: ntfy\n      url: https://x\n      priority:\n        failure: screaming\n",
		"two command bots":         "\nnotify:\n  channels:\n    - type: telegram\n      token: a\n      chat_id: \"1\"\n      commands: true\n    - type: telegram\n      token: b\n      chat_id: \"2\"\n      commands: true\n",
	}
	for name, extra := range cases {
		if _, err := Load(write(t, minimal+extra)); err == nil {
			t.Errorf("%s should have been rejected", name)
		}
	}
}

func TestValidTelegramChannel(t *testing.T) {
	body := minimal + `
notify:
  on: [failure, halt]
  channels:
    - type: telegram
      token: 12345:abcdef
      chat_id: "987654"
      commands: true
    - type: ntfy
      url: https://ntfy.example.com/deploy
      on: [all]
`
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	cmd := cfg.Notify.CommandChannel()
	if cmd == nil || cmd.ChatID != "987654" {
		t.Fatalf("command channel = %+v", cmd)
	}
	// A channel with its own "on" overrides the global list.
	if got := cfg.Notify.Channels[1].Events(cfg.Notify.On); len(got) != 1 || got[0] != "all" {
		t.Errorf("per-channel events = %v", got)
	}
	if got := cmd.Events(cfg.Notify.On); len(got) != 2 {
		t.Errorf("channel without its own list should inherit, got %v", got)
	}
}

func TestHeartbeatValidation(t *testing.T) {
	cfg, err := Load(write(t, minimal+"\nheartbeat:\n  url: https://hc-ping.com/uuid\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Heartbeat.Enabled() {
		t.Fatal("heartbeat should be enabled")
	}
	if cfg.Heartbeat.Method != "GET" || cfg.Heartbeat.Interval == 0 {
		t.Errorf("defaults not applied: %+v", cfg.Heartbeat)
	}

	for name, body := range map[string]string{
		"no scheme":    "\nheartbeat:\n  url: hc-ping.com/uuid\n",
		"too frequent": "\nheartbeat:\n  url: https://x/y\n  interval: 5s\n",
		"bad method":   "\nheartbeat:\n  url: https://x/y\n  method: DELETE\n",
	} {
		if _, err := Load(write(t, minimal+body)); err == nil {
			t.Errorf("%s should have been rejected", name)
		}
	}

	// Without a url the whole block stays inert.
	cfg, err = Load(write(t, minimal))
	if err != nil || cfg.Heartbeat.Enabled() {
		t.Error("no heartbeat configured means no pings")
	}
}

func TestSizeParsing(t *testing.T) {
	cfg, err := Load(write(t, minimal+"\ndefaults:\n  audit:\n    max_size: 25MB\n    keep: 3\n"))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if got := cfg.Defaults.Audit.MaxSize.B(0); got != 25<<20 {
		t.Errorf("max_size = %d, want %d", got, 25<<20)
	}
	if cfg.Defaults.Audit.Keep != 3 {
		t.Errorf("keep = %d", cfg.Defaults.Audit.Keep)
	}

	// Defaults apply when the block is absent.
	cfg, _ = Load(write(t, minimal))
	if cfg.Defaults.Audit.MaxSize.B(0) != 10<<20 || cfg.Defaults.Audit.Keep != 5 {
		t.Errorf("defaults not applied: %+v", cfg.Defaults.Audit)
	}

	if _, err := Load(write(t, minimal+"\ndefaults:\n  audit:\n    max_size: enormous\n")); err == nil {
		t.Error("an unparseable size should be rejected")
	}
}

func TestIncludeDirectory(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "deckhand.yaml")
	if err := os.WriteFile(main, []byte(minimal), 0o600); err != nil {
		t.Fatal(err)
	}
	incDir := main + ".d"
	if err := os.MkdirAll(incDir, 0o750); err != nil {
		t.Fatal(err)
	}
	extra := `
watch:
  - name: api
    repo: acme/api
    trigger:
      type: branch
      branch: main
    path: /tmp/deckhand-test-api
    run:
      - ["true"]
`
	// Lexical order decides, not filesystem order.
	if err := os.WriteFile(filepath.Join(incDir, "20-api.yaml"), []byte(extra), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(incDir, "notes.txt"), []byte("ignored"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(main)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if len(cfg.Watches) != 2 {
		t.Fatalf("got %d watches, want 2 (one from each file)", len(cfg.Watches))
	}
	if cfg.Watches[1].Name != "api" {
		t.Errorf("included watch = %q", cfg.Watches[1].Name)
	}
	if len(cfg.Sources) != 2 {
		t.Errorf("sources = %v, want both files", cfg.Sources)
	}
}

func TestIncludedFilesAreCheckedToo(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "deckhand.yaml")
	_ = os.WriteFile(main, []byte(minimal), 0o600)
	incDir := main + ".d"
	_ = os.MkdirAll(incDir, 0o750)

	// World-writable: someone else could change what runs on this machine.
	// (Written first, then chmod'd, because umask would strip the bits.)
	loose := filepath.Join(incDir, "10-loose.yaml")
	_ = os.WriteFile(loose, []byte("watch: []\n"), 0o600)
	if err := os.Chmod(loose, 0o666); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(main); err == nil || !strings.Contains(err.Error(), "writable") {
		t.Fatalf("a writable included file must be refused, got %v", err)
	}
	_ = os.Chmod(loose, 0o600)

	// Included files may not smuggle in credentials or defaults.
	_ = os.WriteFile(loose, []byte("github:\n  token: sneaky\n"), 0o600)
	if _, err := Load(main); err == nil {
		t.Error("an included file setting github credentials must be refused")
	}
}

func TestDuplicateNamesAcrossIncludes(t *testing.T) {
	dir := t.TempDir()
	main := filepath.Join(dir, "deckhand.yaml")
	_ = os.WriteFile(main, []byte(minimal), 0o600)
	incDir := main + ".d"
	_ = os.MkdirAll(incDir, 0o750)
	_ = os.WriteFile(filepath.Join(incDir, "dup.yaml"), []byte(`
watch:
  - name: app
    repo: acme/other
    trigger: { type: release }
    path: /tmp/other
    run: [["true"]]
`), 0o600)
	if _, err := Load(main); err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("a name reused in an included file must be reported, got %v", err)
	}
}
