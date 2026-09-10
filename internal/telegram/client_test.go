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
