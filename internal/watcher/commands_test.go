package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcwoge/deckhand/internal/config"
	"github.com/marcwoge/deckhand/internal/deploy"
	"github.com/marcwoge/deckhand/internal/telegram"
)

const testConfig = `
version: 1
notify:
  on: [failure]
  channels:
    - type: telegram
      token: bot-token
      chat_id: "12345"
      commands: true
watch:
  - name: shop
    repo: acme/shop
    trigger:
      type: release
    path: %PATH%
    run:
      - ["true"]
`

func testEngine(t *testing.T) *Engine {
	t.Helper()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "deckhand.yaml")
	body := strings.Replace(testConfig, "%PATH%", filepath.Join(dir, "srv"), 1)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatalf("load config: %v", err)
	}
	eng, err := New(cfg, Options{StateDir: filepath.Join(dir, "state"),
		Logf: func(string, string, ...interface{}) {}})
	if err != nil {
		t.Fatalf("new engine: %v", err)
	}
	return eng
}

func TestStatusCommandRendersTheTable(t *testing.T) {
	e := testEngine(t)
	reply, monospace := e.runCommand(context.Background(), "/status")
	if !strings.Contains(reply, "shop") || !strings.Contains(reply, "acme/shop") {
		t.Errorf("reply does not describe the watch:\n%s", reply)
	}
	if !monospace {
		t.Error("the status table must be sent as a fixed-width block or it will not line up")
	}
}

func TestCommandsAreRecognisedWithBotSuffix(t *testing.T) {
	e := testEngine(t)
	// In a group chat Telegram appends the bot name.
	reply, _ := e.runCommand(context.Background(), "/status@deckhand_bot")
	if !strings.Contains(reply, "shop") {
		t.Errorf("a command addressed to the bot must still work:\n%s", reply)
	}
}

func TestUnknownAndMalformedCommands(t *testing.T) {
	e := testEngine(t)
	if reply, _ := e.runCommand(context.Background(), "/frobnicate"); !strings.Contains(reply, "Unknown command") {
		t.Errorf("unknown command reply = %q", reply)
	}
	if reply, _ := e.runCommand(context.Background(), "/deploy"); !strings.Contains(reply, "Usage") {
		t.Errorf("a command missing its argument should explain itself, got %q", reply)
	}
	if reply, _ := e.runCommand(context.Background(), "/deploy nonexistent"); !strings.Contains(reply, "no watch named") {
		t.Errorf("unknown watch reply = %q", reply)
	}
	// Ordinary chatter is not a command and deserves no answer.
	if reply, _ := e.runCommand(context.Background(), "good morning"); reply != "" {
		t.Errorf("plain text should be ignored, got %q", reply)
	}
}

func TestPauseAndResumeThroughCommands(t *testing.T) {
	e := testEngine(t)
	if _, _ = e.runCommand(context.Background(), "/pause database migration"); !e.Paused() {
		t.Fatal("/pause must hold deployments")
	}
	if _, _ = e.runCommand(context.Background(), "/resume"); e.Paused() {
		t.Fatal("/resume must release the hold")
	}
}

// telegramStub records what the bot sends back.
type telegramStub struct {
	mu   sync.Mutex
	sent []string
	srv  *httptest.Server
}

func newTelegramStub(t *testing.T) (*telegram.Client, *telegramStub) {
	t.Helper()
	stub := &telegramStub{}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)
		if text, ok := payload["text"].(string); ok {
			stub.mu.Lock()
			stub.sent = append(stub.sent, text)
			stub.mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(stub.srv.Close)
	c := telegram.New("t")
	c.SetAPI(stub.srv.URL)
	return c, stub
}

func (s *telegramStub) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.sent)
}

func update(chatID int64, text string, sent time.Time) telegram.Update {
	return telegram.Update{UpdateID: 1, Message: &telegram.Message{
		Text: text,
		Date: sent.Unix(),
		Chat: telegram.Chat{ID: chatID, Type: "private"},
		From: telegram.User{ID: chatID},
	}}
}

// Anyone who finds the bot must get nothing at all - not even an error, which
// would confirm the bot exists.
func TestCommandsFromOtherChatsAreIgnored(t *testing.T) {
	e := testEngine(t)
	client, stub := newTelegramStub(t)

	e.handleUpdate(context.Background(), client, "12345", update(99999, "/status", time.Now()))
	if stub.count() != 0 {
		t.Fatalf("a stranger got an answer: %q", stub.sent)
	}

	e.handleUpdate(context.Background(), client, "12345", update(12345, "/status", time.Now()))
	if stub.count() != 1 {
		t.Fatal("the configured chat must be answered")
	}
}

// Telegram keeps undelivered updates for a day. Without an age check, a
// restart could execute a "/deploy" sent yesterday.
func TestStaleCommandsAreRefused(t *testing.T) {
	e := testEngine(t)
	client, stub := newTelegramStub(t)

	e.handleUpdate(context.Background(), client, "12345",
		update(12345, "/deploy shop", time.Now().Add(-2*time.Hour)))

	if stub.count() != 1 {
		t.Fatalf("the sender should be told why nothing happened, got %d replies", stub.count())
	}
	if !strings.Contains(stub.sent[0], "Ignored") {
		t.Errorf("reply = %q, want a refusal", stub.sent[0])
	}
}

func TestOffsetSurvivesRestart(t *testing.T) {
	e := testEngine(t)
	if got := e.loadOffset(); got != 0 {
		t.Errorf("a fresh state should have no offset, got %d", got)
	}
	e.saveOffset(4711)
	if got := e.loadOffset(); got != 4711 {
		t.Errorf("offset = %d, want 4711 so handled commands are not replayed", got)
	}
}

// A full pass through the polling loop: queued messages from before startup
// are skipped, a fresh command is answered, and the offset is persisted.
func TestCommandLoopSkipsBacklogAndAnswers(t *testing.T) {
	e := testEngine(t)

	var mu sync.Mutex
	var replies []string
	round := 0

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)

		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			mu.Lock()
			replies = append(replies, payload["text"].(string))
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		}
		// getUpdates
		mu.Lock()
		round++
		n := round
		mu.Unlock()
		switch n {
		case 1: // the backlog probe, made with timeout 0
			_, _ = w.Write([]byte(`{"ok":true,"result":[
				{"update_id":100,"message":{"text":"/deploy shop","date":1,
				 "chat":{"id":12345,"type":"private"}}}]}`))
		case 2: // a real command, sent now
			fmt.Fprintf(w, `{"ok":true,"result":[
				{"update_id":101,"message":{"text":"/status","date":%d,
				 "chat":{"id":12345,"type":"private"}}}]}`, time.Now().Unix())
		default:
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		}
	}))
	defer srv.Close()

	client := telegram.New("t")
	client.SetAPI(srv.URL)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.runCommandBotWith(ctx, client, "12345")
	}()

	deadline := time.After(3 * time.Second)
	for {
		mu.Lock()
		n := len(replies)
		mu.Unlock()
		if n > 0 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			t.Fatal("the bot never answered /status")
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	// The stale "/deploy shop" from the backlog must never have been executed.
	for _, r := range replies {
		if strings.Contains(r, "Deploying") {
			t.Fatal("a command queued before startup was executed")
		}
	}
	if !strings.Contains(replies[0], "shop") {
		t.Errorf("first reply = %q, want the status table", replies[0])
	}
	if e.loadOffset() < 102 {
		t.Errorf("offset = %d, want 102 or more so commands are not replayed", e.loadOffset())
	}
}

func TestReloadPicksUpANewWatch(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "deckhand.yaml")
	body := strings.Replace(testConfig, "%PATH%", filepath.Join(dir, "srv"), 1)
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	e, err := New(cfg, Options{StateDir: filepath.Join(dir, "state"),
		Logf: func(string, string, ...interface{}) {}})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.start(ctx); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer e.stop()

	if len(e.Watches()) != 1 {
		t.Fatalf("expected one watch to begin with, got %d", len(e.Watches()))
	}

	// Add a second watch and reload.
	body += `
  - name: api
    repo: acme/api
    trigger:
      type: branch
      branch: main
    path: ` + filepath.Join(dir, "api") + `
    run:
      - ["true"]
`
	if err := os.WriteFile(cfgPath, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.Reload(ctx); err != nil {
		t.Fatalf("reload: %v", err)
	}
	if len(e.Watches()) != 2 {
		t.Fatalf("after reload: %d watches, want 2", len(e.Watches()))
	}
	if _, err := e.Watch("api"); err != nil {
		t.Errorf("the new watch is not known: %v", err)
	}
}

// A typo must not take the worker down.
func TestReloadKeepsRunningConfigOnError(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "deckhand.yaml")
	body := strings.Replace(testConfig, "%PATH%", filepath.Join(dir, "srv"), 1)
	_ = os.WriteFile(cfgPath, []byte(body), 0o600)
	cfg, _ := config.Load(cfgPath)
	e, err := New(cfg, Options{StateDir: filepath.Join(dir, "state"),
		Logf: func(string, string, ...interface{}) {}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := e.start(ctx); err != nil {
		t.Fatal(err)
	}
	defer e.stop()

	_ = os.WriteFile(cfgPath, []byte("version: 1\nthis is not: [valid\n"), 0o600)
	if err := e.Reload(ctx); err == nil {
		t.Fatal("an invalid configuration must be reported")
	} else if !strings.Contains(err.Error(), "keeping the running one") {
		t.Errorf("error should say the old config is still in use, got %v", err)
	}
	if _, err := e.Watch("shop"); err != nil {
		t.Errorf("the original watch must still be configured: %v", err)
	}
}

// A failed deployment must roll back to the revision that was running before
// it, not to the one before that. The state is untouched by a failure, so the
// running revision is CurrentRelease - reaching for PreviousRelease here would
// skip a version back.
func TestAutomaticRollbackTargetsTheRunningRevision(t *testing.T) {
	e := testEngine(t)
	w, err := e.Watch("shop")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	st := &deploy.State{
		CurrentRelease:  filepath.Join(dir, "running"),
		LastSHA:         "2222222222222222222222222222222222222222",
		PreviousRelease: filepath.Join(dir, "older"),
		PreviousSHA:     "1111111111111111111111111111111111111111",
	}
	for _, d := range []string{st.CurrentRelease, st.PreviousRelease} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}

	if !hasRunning(w, st) {
		t.Fatal("a live release must be recognised as a rollback target")
	}
	// Drop the running release: then there is nothing to roll back to, and the
	// older one must not be used as a substitute.
	if err := os.RemoveAll(st.CurrentRelease); err != nil {
		t.Fatal(err)
	}
	if hasRunning(w, st) {
		t.Error("a missing release must not count as a rollback target")
	}
	if !hasPrevious(w, st) {
		t.Error("the manual rollback still has its own target")
	}
}

// The anonymous slowdown exists for github.com's 60-per-hour cap. A GitHub
// Enterprise server or a mirror has its own limits, so the configured interval
// must survive there.
func TestAnonymousSlowdownOnlyAppliesToGitHubCom(t *testing.T) {
	for api, want := range map[string]bool{
		"":                               true,
		"https://api.github.com":         true,
		"https://ghe.example.com/api/v3": false,
		"http://127.0.0.1:8799":          false,
	} {
		if got := isPublicGitHub(api); got != want {
			t.Errorf("isPublicGitHub(%q) = %v, want %v", api, got, want)
		}
	}
}
