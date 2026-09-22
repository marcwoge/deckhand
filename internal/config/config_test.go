package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
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
	if w.Auth.Mode != "token" || w.Auth.Own() {
		t.Errorf("auth = %+v, want the global token", w.Auth)
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

func TestGitHubAppConfig(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	if err := os.WriteFile(keyPath, []byte("-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(minimal, "version: 1", "version: 1\ngithub:\n  app:\n    id: \"123456\"\n    private_key_file: "+keyPath+"\n", 1)
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if cfg.GitHub.App == nil || cfg.GitHub.App.ID != "123456" {
		t.Fatalf("app config = %+v", cfg.GitHub.App)
	}
	key, err := cfg.GitHub.App.ResolvePrivateKey()
	if err != nil || !strings.Contains(string(key), "BEGIN RSA PRIVATE KEY") {
		t.Errorf("private key not resolved: %v", err)
	}
}

func TestGitHubAppValidation(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "app.pem")
	_ = os.WriteFile(keyPath, []byte("-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----\n"), 0o600)

	cases := map[string]string{
		"no id":           "github:\n  app:\n    private_key_file: " + keyPath + "\n",
		"no key":          "github:\n  app:\n    id: \"1\"\n",
		"two key sources": "github:\n  app:\n    id: \"1\"\n    private_key_file: " + keyPath + "\n    private_key_env: SOME_VAR\n",
		"token and app":   "github:\n  token: ghp_x\n  app:\n    id: \"1\"\n    private_key_file: " + keyPath + "\n",
	}
	for name, extra := range cases {
		body := strings.Replace(minimal, "version: 1", "version: 1\n"+extra, 1)
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s should have been rejected", name)
		}
	}

	// A key file others can read must be refused, like the token file.
	loose := filepath.Join(dir, "loose.pem")
	_ = os.WriteFile(loose, []byte("-----BEGIN RSA PRIVATE KEY-----\nx\n-----END RSA PRIVATE KEY-----\n"), 0o600)
	if err := os.Chmod(loose, 0o644); err != nil {
		t.Fatal(err)
	}
	body := strings.Replace(minimal, "version: 1",
		"version: 1\ngithub:\n  app:\n    id: \"1\"\n    private_key_file: "+loose+"\n", 1)
	if _, err := Load(write(t, body)); err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Errorf("a world-readable app key must be refused, got %v", err)
	}
}

func TestImageTriggerConfig(t *testing.T) {
	body := `
version: 1
registry:
  ghcr.io:
    username: marcwoge
    password_env: DECKHAND_GHCR_TOKEN
watch:
  - name: shop
    trigger:
      type: image
      image: ghcr.io/marcwoge/shop
    path: /srv/shop
    run:
      - ["docker", "compose", "up", "-d"]
`
	cfg, err := Load(write(t, body))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	w := cfg.Watches[0]
	if w.Trigger.Tag != "latest" {
		t.Errorf("tag = %q, want the latest default", w.Trigger.Tag)
	}
	if w.NeedsCheckout() {
		t.Error("an image trigger must not check anything out")
	}
	if w.Subject() != "ghcr.io/marcwoge/shop" {
		t.Errorf("Subject() = %q", w.Subject())
	}
	if cfg.Registry["ghcr.io"].Username != "marcwoge" {
		t.Errorf("registry credentials not parsed: %+v", cfg.Registry)
	}
}

func TestImageTriggerValidation(t *testing.T) {
	base := `
version: 1
watch:
  - name: shop
    trigger:
      type: image
      image: ghcr.io/marcwoge/shop
%s    path: /srv/shop
    run:
      - ["true"]
`
	cases := map[string]string{
		"with repo":         "    repo: acme/shop\n",
		"tag and tag_match": "      tag: latest\n      tag_match: \"v*\"\n",
		"with branch":       "      branch: main\n",
		"with shared":       "    shared: [\".env\"]\n",
		"with strategy":     "    strategy: releases\n",
	}
	for name, extra := range cases {
		if _, err := Load(write(t, fmt.Sprintf(base, extra))); err == nil {
			t.Errorf("%s should have been rejected", name)
		}
	}
	// A digest-pinned image defeats the point of watching.
	pinned := strings.Replace(fmt.Sprintf(base, ""), "ghcr.io/marcwoge/shop",
		"ghcr.io/marcwoge/shop@sha256:abc", 1)
	if _, err := Load(write(t, pinned)); err == nil {
		t.Error("a digest-pinned image should have been rejected")
	}
	// An image trigger without an image is meaningless.
	noImage := strings.Replace(fmt.Sprintf(base, ""), "      image: ghcr.io/marcwoge/shop\n", "", 1)
	if _, err := Load(write(t, noImage)); err == nil {
		t.Error("an image trigger without an image should have been rejected")
	}
}

// auth: accepted both short forms long before the block form existed, so both
// must keep parsing exactly as they did.
func TestWatchAuthShortForms(t *testing.T) {
	cfg, err := Load(write(t, `
version: 1
watch:
  - name: a
    repo: acme/a
    auth: none
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
  - name: b
    repo: acme/b
    auth: token
    trigger: { type: release }
    path: /tmp/deckhand-test-b
    run: [["true"]]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if !cfg.Watches[0].Auth.Anonymous() {
		t.Error(`auth: none must mean anonymous`)
	}
	if cfg.Watches[1].Auth.Anonymous() || cfg.Watches[1].Auth.Own() {
		t.Error(`auth: token must mean the global credential`)
	}
}

func TestWatchAuthBlockForm(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("per-repo-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(write(t, fmt.Sprintf(`
version: 1
watch:
  - name: a
    repo: acme/a
    auth:
      token_file: %s
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
`, tokenFile)))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	auth := cfg.Watches[0].Auth
	if !auth.Own() {
		t.Fatal("the watch must be seen as bringing its own credential")
	}
	got, err := auth.ResolveToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "per-repo-token" {
		t.Errorf("token = %q, want the file contents", got)
	}
	if !strings.Contains(auth.Describe(), tokenFile) {
		t.Errorf("describe = %q, want the source named", auth.Describe())
	}
	if strings.Contains(auth.Describe(), "per-repo-token") {
		t.Error("describe must never contain the credential itself")
	}
}

// A token file others can read is refused for a per-watch credential exactly as
// it is for the global one - and at load time, not at the first deployment.
func TestWatchAuthRefusesLooseTokenFile(t *testing.T) {
	dir := t.TempDir()
	tokenFile := filepath.Join(dir, "token")
	if err := os.WriteFile(tokenFile, []byte("secret\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(tokenFile, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(write(t, fmt.Sprintf(`
version: 1
watch:
  - name: a
    repo: acme/a
    auth:
      token_file: %s
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
`, tokenFile)))
	if err == nil || !strings.Contains(err.Error(), "readable by others") {
		t.Fatalf("error = %v, want a refusal naming the permissions", err)
	}
}

func TestWatchAuthRejectsContradictions(t *testing.T) {
	cases := map[string]string{
		"two sources": `
    auth:
      token: a
      token_env: B`,
		"none plus a credential": `
    auth:
      mode: none
      token: a`,
		"nonsense mode": `
    auth: sometimes`,
	}
	for name, block := range cases {
		body := `
version: 1
watch:
  - name: a
    repo: acme/a` + block + `
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
`
		if _, err := Load(write(t, body)); err == nil {
			t.Errorf("%s: must be refused", name)
		}
	}
}

// systemd decrypts LoadCredentialEncrypted into $CREDENTIALS_DIRECTORY, a tmpfs
// only the service can read. Pointing the configuration at it is the one way to
// have a credential encrypted at rest without Deckhand holding the key, so the
// variable has to be expanded in a path.
func TestCredentialsDirectoryIsExpanded(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CREDENTIALS_DIRECTORY", dir)
	if err := os.WriteFile(filepath.Join(dir, "github-token"), []byte("ghp_tpm\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(write(t, `
version: 1
github:
  token_file: ${CREDENTIALS_DIRECTORY}/github-token
watch:
  - name: a
    repo: acme/a
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	got, err := cfg.GitHub.ResolveToken()
	if err != nil {
		t.Fatal(err)
	}
	if got != "ghp_tpm" {
		t.Errorf("token = %q, want the one from the credentials directory", got)
	}
}

// A credential command cannot be validated while parsing, but its shape can.
func TestTokenCommandValidation(t *testing.T) {
	if _, err := Load(write(t, `
version: 1
github:
  token_env: A
  token_command: ["pass", "show", "deckhand/github"]
watch:
  - name: a
    repo: acme/a
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
`)); err == nil {
		t.Error("a token from two sources must be refused")
	}

	cfg, err := Load(write(t, `
version: 1
github:
  token_command: ["pass", "show", "deckhand/github"]
  token_ttl: 15m
watch:
  - name: a
    repo: acme/a
    trigger: { type: release }
    path: /tmp/deckhand-test-a
    run: [["true"]]
`))
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	spec := cfg.GitHub.TokenSpec()
	if !spec.FromCommand() {
		t.Error("the spec must report a command source")
	}
	if spec.TTL != 15*time.Minute {
		t.Errorf("ttl = %s, want 15m", spec.TTL)
	}
	// Describe goes into logs and check output, and an argument can itself be
	// the credential.
	if strings.Contains(spec.Describe(), "deckhand/github") {
		t.Errorf("describe = %q, want the arguments left out", spec.Describe())
	}
}
