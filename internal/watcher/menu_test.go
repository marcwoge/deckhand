package watcher

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/marcwoge/deckhand/internal/telegram"
)

// buttonData collects every callback_data in a keyboard, so a test can assert
// on what a screen offers without depending on the labels.
func buttonData(k telegram.Keyboard) []string {
	var out []string
	for _, row := range k {
		for _, b := range row {
			out = append(out, b.Data)
		}
	}
	return out
}

func hasData(k telegram.Keyboard, want string) bool {
	for _, got := range buttonData(k) {
		if got == want {
			return true
		}
	}
	return false
}

func TestHomeViewReflectsPauseState(t *testing.T) {
	e := testEngine(t)

	v := e.homeView("")
	if !hasData(v.keys, "a:pause") {
		t.Errorf("an active engine must offer pause, got %v", buttonData(v.keys))
	}
	if !strings.Contains(v.text, "active") {
		t.Errorf("text = %q, want the active state", v.text)
	}

	if err := e.Pause("test"); err != nil {
		t.Fatal(err)
	}
	v = e.homeView("")
	if !hasData(v.keys, "a:resume") || hasData(v.keys, "a:pause") {
		t.Errorf("a paused engine must offer resume only, got %v", buttonData(v.keys))
	}
}

// Callback data is limited to 64 bytes, which is why watches are addressed by
// index. A long watch name must not be able to break a button.
func TestCallbackDataStaysWithinTheLimit(t *testing.T) {
	e := testEngine(t)
	views := []view{
		e.homeView("a notice"),
		e.navView("status"),
		e.navView("history"),
		e.navView("deploy"),
		e.navView("rollback"),
		e.confirmView("deploy", "0"),
	}
	for _, v := range views {
		for _, data := range buttonData(v.keys) {
			if len(data) > 64 {
				t.Errorf("callback data %q is %d bytes, over the API limit", data, len(data))
			}
		}
	}
}

func TestMenuNavigationShowsWatchesAndStatus(t *testing.T) {
	e := testEngine(t)

	v := e.navView("deploy")
	if !hasData(v.keys, "n:deploy:0") {
		t.Errorf("the deploy screen must list the watches, got %v", buttonData(v.keys))
	}

	v = e.navView("status")
	if !v.mono {
		t.Error("the status table must be sent as a fixed-width block")
	}
	if !strings.Contains(v.text, "shop") {
		t.Errorf("status = %q, want the watch in it", v.text)
	}

	v = e.navView("history:0")
	if !v.mono {
		t.Error("history must be sent as a fixed-width block")
	}
}

// A confirmation button must work once. The same button, found in the chat
// history a day later, must deploy nothing.
func TestConfirmationTokenIsSingleUse(t *testing.T) {
	e := testEngine(t)

	v := e.confirmView("rollback", "0")
	data := buttonData(v.keys)
	if len(data) == 0 || !strings.HasPrefix(data[0], "x:") {
		t.Fatalf("no execute button in %v", data)
	}
	token := strings.TrimPrefix(data[0], "x:")

	if _, ok := e.menu.spend(token); !ok {
		t.Fatal("the freshly armed token must work")
	}
	if _, ok := e.menu.spend(token); ok {
		t.Fatal("a token must not work twice")
	}
}

func TestExpiredTokenIsRefused(t *testing.T) {
	e := testEngine(t)
	token, err := e.menu.arm("deploy", "shop")
	if err != nil {
		t.Fatal(err)
	}
	e.menu.mu.Lock()
	a := e.menu.actions[token]
	a.created = time.Now().Add(-2 * pendingTTL)
	e.menu.actions[token] = a
	e.menu.mu.Unlock()

	if _, ok := e.menu.spend(token); ok {
		t.Fatal("a token older than its TTL must be refused")
	}

	v := e.menuView(context.Background(), "x:"+token)
	if !strings.Contains(v.text, "expired") {
		t.Errorf("view = %q, want an explanation", v.text)
	}
}

// An index that no longer exists after a reload must not panic or act on the
// wrong watch.
func TestUnknownWatchIndexIsHandled(t *testing.T) {
	e := testEngine(t)
	for _, data := range []string{"n:deploy:99", "n:rollback:-1", "n:history:x", "n:nonsense", "junk"} {
		v := e.menuView(context.Background(), data)
		if v.text == "" {
			t.Errorf("%q produced an empty screen", data)
		}
		if strings.Contains(v.text, "Deploy shop") {
			t.Errorf("%q resolved to a watch it must not", data)
		}
	}
}

// menuStub records the API calls a button press causes.
type menuStub struct {
	mu    sync.Mutex
	calls map[string]int
	edits []string
	srv   *httptest.Server
}

func newMenuStub(t *testing.T) (*telegram.Client, *menuStub) {
	t.Helper()
	stub := &menuStub{calls: map[string]int{}}
	stub.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)

		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]
		stub.mu.Lock()
		stub.calls[method]++
		if method == "editMessageText" {
			stub.edits = append(stub.edits, payload["text"].(string))
		}
		stub.mu.Unlock()
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	t.Cleanup(stub.srv.Close)
	c := telegram.New("t")
	c.SetAPI(stub.srv.URL)
	return c, stub
}

func (s *menuStub) count(method string) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.calls[method]
}

func callback(chatID int64, data string) telegram.Update {
	return telegram.Update{UpdateID: 1, CallbackQuery: &telegram.CallbackQuery{
		ID:   "cb1",
		From: telegram.User{ID: chatID},
		Data: data,
		Message: &telegram.Message{MessageID: 7,
			Chat: telegram.Chat{ID: chatID, Type: "private"}},
	}}
}

func TestButtonPressIsAcknowledgedAndRewritesTheMenu(t *testing.T) {
	e := testEngine(t)
	client, stub := newMenuStub(t)

	e.handleUpdate(context.Background(), client, "12345", callback(12345, "n:status"))

	if stub.count("answerCallbackQuery") != 1 {
		t.Error("a press must be acknowledged or the button spins forever")
	}
	if stub.count("editMessageText") != 1 {
		t.Error("the menu must rewrite itself instead of sending a new message")
	}
}

// The chat check must cover buttons too: a forwarded message carries its
// buttons with it.
func TestButtonPressFromAnotherChatIsRefused(t *testing.T) {
	e := testEngine(t)
	client, stub := newMenuStub(t)

	e.handleUpdate(context.Background(), client, "12345", callback(99999, "a:pause"))

	if e.Paused() {
		t.Fatal("a stranger paused the engine")
	}
	if stub.count("editMessageText") != 0 {
		t.Error("nothing must be shown to a chat that is not configured")
	}
	if stub.count("answerCallbackQuery") != 1 {
		t.Error("the press is still acknowledged, so the button stops spinning")
	}
}

func TestMenuCommandSendsButtons(t *testing.T) {
	e := testEngine(t)
	client, stub := newMenuStub(t)

	e.handleUpdate(context.Background(), client, "12345", update(12345, "/menu", time.Now()))
	if stub.count("sendMessage") != 1 {
		t.Fatal("/menu must answer")
	}
	e.handleUpdate(context.Background(), client, "12345", update(12345, "/start", time.Now()))
	if stub.count("sendMessage") != 2 {
		t.Fatal("/start must open the menu as well")
	}
}

func TestPauseAndResumeThroughButtons(t *testing.T) {
	e := testEngine(t)
	ctx := context.Background()

	if v := e.menuView(ctx, "a:pause"); !e.Paused() {
		t.Fatalf("the pause button did not pause, screen was %q", v.text)
	}
	if v := e.menuView(ctx, "a:resume"); e.Paused() {
		t.Fatalf("the resume button did not resume, screen was %q", v.text)
	}
}

func TestBotCommandsAreValid(t *testing.T) {
	for _, c := range botCommands() {
		if strings.HasPrefix(c.Command, "/") {
			t.Errorf("%q must be given without the slash", c.Command)
		}
		if c.Description == "" {
			t.Errorf("%q needs a description", c.Command)
		}
	}
}

// A full pass through the polling loop with real Telegram JSON: "/menu" is
// answered with buttons, and pressing through Deploy → a watch → the
// confirmation actually reaches the deploy path. Unit tests cover the screens;
// this covers the wiring between them, which is where the bugs were.
func TestMenuLoopEndToEnd(t *testing.T) {
	e := testEngine(t)

	var mu sync.Mutex
	var sent, edited []string
	var lastKeyboard []interface{}
	answers := 0
	round := 0

	// nextPress picks the button to press from the keyboard just rendered: the
	// first one whose data starts with the wanted prefix.
	nextPress := func(prefix string) string {
		mu.Lock()
		defer mu.Unlock()
		for _, row := range lastKeyboard {
			for _, b := range row.([]interface{}) {
				data, _ := b.(map[string]interface{})["callback_data"].(string)
				if strings.HasPrefix(data, prefix) {
					return data
				}
			}
		}
		return ""
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var payload map[string]interface{}
		_ = json.Unmarshal(body, &payload)
		method := r.URL.Path[strings.LastIndexByte(r.URL.Path, '/')+1:]

		record := func(into *[]string) {
			mu.Lock()
			defer mu.Unlock()
			*into = append(*into, payload["text"].(string))
			lastKeyboard = nil
			if markup, ok := payload["reply_markup"].(map[string]interface{}); ok {
				lastKeyboard, _ = markup["inline_keyboard"].([]interface{})
			}
		}

		switch method {
		case "sendMessage":
			record(&sent)
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		case "editMessageText":
			record(&edited)
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		case "answerCallbackQuery":
			mu.Lock()
			answers++
			mu.Unlock()
			_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
			return
		}

		// getUpdates: walk the menu one press per round.
		mu.Lock()
		round++
		n := round
		mu.Unlock()

		press := func(id int, data string) {
			if data == "" {
				t.Errorf("round %d: no button to press", n)
			}
			fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":%d,"callback_query":{
				"id":"cb%d","from":{"id":12345},"data":%q,
				"message":{"message_id":7,"chat":{"id":12345,"type":"private"}}}}]}`,
				id, id, data)
		}

		switch n {
		case 1: // the backlog probe
			_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
		case 2:
			fmt.Fprintf(w, `{"ok":true,"result":[{"update_id":200,"message":{
				"message_id":1,"text":"/menu","date":%d,
				"chat":{"id":12345,"type":"private"}}}]}`, time.Now().Unix())
		case 3:
			press(201, nextPress("n:deploy"))
		case 4:
			press(202, nextPress("n:deploy:"))
		case 5:
			press(203, nextPress("x:"))
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

	deadline := time.After(5 * time.Second)
	for {
		mu.Lock()
		n := len(edited)
		mu.Unlock()
		if n >= 3 {
			break
		}
		select {
		case <-deadline:
			cancel()
			<-done
			mu.Lock()
			t.Fatalf("the menu never got through: sent=%v edited=%v", sent, edited)
		case <-time.After(10 * time.Millisecond):
		}
	}
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if len(sent) != 1 || !strings.Contains(sent[0], "deckhand") {
		t.Fatalf("/menu answer = %v, want the home screen", sent)
	}
	if !strings.Contains(edited[0], "which watch") {
		t.Errorf("first press = %q, want the watch list", edited[0])
	}
	if !strings.Contains(edited[1], "Deploy shop now") {
		t.Errorf("second press = %q, want the confirmation", edited[1])
	}
	if !strings.Contains(edited[2], "Deploying shop") {
		t.Errorf("third press = %q, want the deploy to have started", edited[2])
	}
	if answers != 3 {
		t.Errorf("acknowledged %d of 3 presses; an unacknowledged button spins forever", answers)
	}
}
