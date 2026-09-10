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
