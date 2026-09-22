package telegram

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestSendMessage(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/sendMessage") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("test-token")
	c.SetAPI(srv.URL)
	if err := c.Send(context.Background(), "12345", "deployment failed", false); err != nil {
		t.Fatalf("send: %v", err)
	}
	if got["chat_id"] != "12345" || got["text"] != "deployment failed" {
		t.Errorf("payload = %+v", got)
	}
}

func TestMonospaceWrapsInACodeBlock(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	_ = c.Send(context.Background(), "1", "WATCH  REPO\nshop   acme/shop", true)

	text, _ := got["text"].(string)
	if !strings.HasPrefix(text, "```") || !strings.HasSuffix(text, "```") {
		t.Errorf("status tables must be sent as a fixed-width block, got %q", text)
	}
}

func TestLongMessagesAreTruncatedNotRejected(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	if err := c.Send(context.Background(), "1", strings.Repeat("x", 9000), false); err != nil {
		t.Fatalf("a long message must still be delivered: %v", err)
	}
	if text, _ := got["text"].(string); len(text) > maxMessage {
		t.Errorf("message of %d bytes exceeds the API limit", len(text))
	}
}

func TestAPIErrorsAreReadable(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"ok":false,"error_code":401,"description":"Unauthorized"}`))
	}))
	defer srv.Close()

	c := New("bad-token")
	c.SetAPI(srv.URL)
	err := c.Send(context.Background(), "1", "hi", false)
	if err == nil || !strings.Contains(err.Error(), "token rejected") {
		t.Fatalf("error = %v, want a clear statement about the token", err)
	}
	if strings.Contains(err.Error(), "bad-token") {
		t.Errorf("the bot token leaked into an error: %v", err)
	}
}

func TestGetUpdatesPassesOffset(t *testing.T) {
	var got map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &got)
		_, _ = w.Write([]byte(`{"ok":true,"result":[
			{"update_id":42,"message":{"message_id":1,"text":"/status","date":1700000000,
			 "chat":{"id":12345,"type":"private"},"from":{"id":12345}}}]}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	updates, err := c.GetUpdates(context.Background(), 41, 30)
	if err != nil {
		t.Fatalf("getUpdates: %v", err)
	}
	if got["offset"] != float64(41) {
		t.Errorf("offset = %v, want 41 so handled messages are not replayed", got["offset"])
	}
	if len(updates) != 1 || updates[0].Message.Text != "/status" {
		t.Fatalf("updates = %+v", updates)
	}
	if updates[0].Message.Chat.ID != 12345 {
		t.Errorf("chat id not parsed: %+v", updates[0].Message.Chat)
	}
}

// A keyboard has to reach the API as inline_keyboard, or the buttons simply do
// not appear.
func TestSendWithKeyboard(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	keys := Keyboard{{{Text: "Status", Data: "n:status"}}}
	if err := c.SendWithKeyboard(context.Background(), "1", "hello", false, keys); err != nil {
		t.Fatal(err)
	}

	markup, ok := payload["reply_markup"].(map[string]interface{})
	if !ok {
		t.Fatalf("no reply_markup in %v", payload)
	}
	rows, ok := markup["inline_keyboard"].([]interface{})
	if !ok || len(rows) != 1 {
		t.Fatalf("inline_keyboard = %v, want one row", markup["inline_keyboard"])
	}
	button := rows[0].([]interface{})[0].(map[string]interface{})
	if button["callback_data"] != "n:status" {
		t.Errorf("callback_data = %v, want n:status", button["callback_data"])
	}
}

// A message without buttons must not carry an empty markup, which Telegram
// would use to strip buttons from a message that never had any.
func TestSendWithoutKeyboardOmitsMarkup(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	if err := c.Send(context.Background(), "1", "hello", false); err != nil {
		t.Fatal(err)
	}
	if _, ok := payload["reply_markup"]; ok {
		t.Errorf("reply_markup must be absent, got %v", payload["reply_markup"])
	}
}

// Pressing "Refresh" on an unchanged status table is normal. Telegram answers
// with an error, which must not be reported as a failure.
func TestEditMessageIgnoresUnchangedText(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,` +
			`"description":"Bad Request: message is not modified"}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	if err := c.EditMessage(context.Background(), "1", 7, "same", false, nil); err != nil {
		t.Errorf("an unchanged edit must be accepted, got %v", err)
	}
}

// An edit without a keyboard must clear the old buttons, otherwise a spent
// confirmation button stays pressable on screen.
func TestEditMessageClearsButtons(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	if err := c.EditMessage(context.Background(), "1", 7, "done", false, nil); err != nil {
		t.Fatal(err)
	}
	markup, ok := payload["reply_markup"].(map[string]interface{})
	if !ok {
		t.Fatalf("no reply_markup in %v", payload)
	}
	if rows, _ := markup["inline_keyboard"].([]interface{}); len(rows) != 0 {
		t.Errorf("inline_keyboard = %v, want it emptied", rows)
	}
}

// Telegram rejects a notice longer than 200 characters outright, which would
// turn a successful action into a visible error.
func TestAnswerCallbackTruncatesNotice(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	if err := c.AnswerCallback(context.Background(), "cb", strings.Repeat("x", 500)); err != nil {
		t.Fatal(err)
	}
	if got := payload["text"].(string); len(got) != maxNotice {
		t.Errorf("notice length = %d, want %d", len(got), maxNotice)
	}
}

// A button press only arrives when callback_query is in allowed_updates.
func TestGetUpdatesAsksForCallbacks(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":[]}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	if _, err := c.GetUpdates(context.Background(), 0, 0); err != nil {
		t.Fatal(err)
	}
	allowed, _ := payload["allowed_updates"].([]interface{})
	found := false
	for _, a := range allowed {
		if a == "callback_query" {
			found = true
		}
	}
	if !found {
		t.Errorf("allowed_updates = %v, want callback_query in it", allowed)
	}
}

// Telegram rejects the whole list if a command carries its slash, so the shape
// matters more than the content.
func TestSetCommands(t *testing.T) {
	var payload map[string]interface{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/setMyCommands") {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &payload)
		_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
	}))
	defer srv.Close()

	c := New("t")
	c.SetAPI(srv.URL)
	err := c.SetCommands(context.Background(), []Command{{Command: "menu", Description: "buttons"}})
	if err != nil {
		t.Fatal(err)
	}
	list, _ := payload["commands"].([]interface{})
	if len(list) != 1 {
		t.Fatalf("commands = %v, want one", payload["commands"])
	}
	entry := list[0].(map[string]interface{})
	if entry["command"] != "menu" || entry["description"] != "buttons" {
		t.Errorf("entry = %v, want command and description", entry)
	}
}
