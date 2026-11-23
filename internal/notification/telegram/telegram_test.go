package telegram

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestDirectRequestAndTelegramResponse(t *testing.T) {
	var path string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		if r.Method != http.MethodPost || r.Header.Get("Accept") != "application/json" {
			t.Errorf("request headers/method: %s %v", r.Method, r.Header)
		}
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body["chat_id"] != "-100123" || body["text"] != "hello" || body["token"] != "" || body["message"] != "" {
			t.Errorf("direct body = %#v", body)
		}
		w.Write([]byte(`{"ok":true,"result":{}}`))
	}))
	defer server.Close()
	sender, err := New(Config{URL: server.URL, Token: "secret", ChatID: "-100123", Enabled: true, Timeout: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err = sender.Send(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if path != "/botsecret/sendMessage" {
		t.Fatalf("path = %q", path)
	}
}

func TestDirectResponseMustHaveSuccess(t *testing.T) {
	for _, response := range []string{`{"ok":false,"error_code":403,"description":"bot blocked"}`, `{"unexpected":true}`, "not json"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(response)) }))
		sender, err := New(Config{URL: server.URL, Token: "secret", ChatID: "123", Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		err = sender.Send(context.Background(), "hello")
		server.Close()
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("response %q error = %v", response, err)
		}
	}
}

func TestDirectURLRejectsMethodEndpoint(t *testing.T) {
	if _, err := New(Config{URL: "https://api.telegram.org/sendMessage", Enabled: true}); err == nil {
		t.Fatal("sendMessage method endpoint accepted as base URL")
	}
	if _, err := New(Config{URL: DirectURL, Enabled: false}); err != nil {
		t.Fatalf("disabled default config should be valid: %v", err)
	}
}

func TestParseChatIDPreservesNegativeAndUsername(t *testing.T) {
	for input, want := range map[string]string{"-100123": "-100123", "@channel_name": "@channel_name"} {
		got, err := ParseChatID(input)
		if err != nil || got != want {
			t.Errorf("ParseChatID(%q) = %q, %v", input, got, err)
		}
	}
}
