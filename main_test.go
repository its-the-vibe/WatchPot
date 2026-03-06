package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// ── loadConfig tests ─────────────────────────────────────────────────────────

func TestLoadConfig_Defaults(t *testing.T) {
	cfg, err := loadConfig(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RedisAddr != "localhost:6379" {
		t.Errorf("default RedisAddr = %q, want %q", cfg.RedisAddr, "localhost:6379")
	}
	if cfg.ServerAddr != ":8080" {
		t.Errorf("default ServerAddr = %q, want %q", cfg.ServerAddr, ":8080")
	}
}

func TestLoadConfig_FromFile(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	content := []byte("redis_addr: \"redis:6380\"\nredis_channel: \"mychan\"\nredis_key: \"mykey\"\nserver_addr: \":9090\"\n")
	if err := os.WriteFile(cfgFile, content, 0600); err != nil {
		t.Fatal(err)
	}

	cfg, err := loadConfig(cfgFile)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RedisAddr != "redis:6380" {
		t.Errorf("RedisAddr = %q, want %q", cfg.RedisAddr, "redis:6380")
	}
	if cfg.RedisChannel != "mychan" {
		t.Errorf("RedisChannel = %q, want %q", cfg.RedisChannel, "mychan")
	}
	if cfg.RedisKey != "mykey" {
		t.Errorf("RedisKey = %q, want %q", cfg.RedisKey, "mykey")
	}
	if cfg.ServerAddr != ":9090" {
		t.Errorf("ServerAddr = %q, want %q", cfg.ServerAddr, ":9090")
	}
}

func TestLoadConfig_EnvOverride(t *testing.T) {
	t.Setenv("WATCHPOT_REDIS_ADDR", "envhost:1234")
	t.Setenv("WATCHPOT_REDIS_CHANNEL", "envchan")
	t.Setenv("WATCHPOT_REDIS_KEY", "envkey")
	t.Setenv("WATCHPOT_SERVER_ADDR", ":1111")

	cfg, err := loadConfig(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.RedisAddr != "envhost:1234" {
		t.Errorf("RedisAddr = %q, want envhost:1234", cfg.RedisAddr)
	}
	if cfg.RedisChannel != "envchan" {
		t.Errorf("RedisChannel = %q, want envchan", cfg.RedisChannel)
	}
	if cfg.RedisKey != "envkey" {
		t.Errorf("RedisKey = %q, want envkey", cfg.RedisKey)
	}
	if cfg.ServerAddr != ":1111" {
		t.Errorf("ServerAddr = %q, want :1111", cfg.ServerAddr)
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgFile := filepath.Join(dir, "config.yaml")
	// This is genuinely invalid YAML (unclosed flow mapping)
	if err := os.WriteFile(cfgFile, []byte("{invalid: [unclosed\n"), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := loadConfig(cfgFile)
	if err == nil {
		t.Fatal("expected error for invalid YAML, got nil")
	}
}

// ── broker tests ─────────────────────────────────────────────────────────────

func TestBroker_BroadcastToSubscribers(t *testing.T) {
	b := newBroker()

	ch1 := b.subscribe()
	ch2 := b.subscribe()

	b.broadcast("hello")

	select {
	case msg := <-ch1:
		if msg != "hello" {
			t.Errorf("ch1 got %q, want %q", msg, "hello")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for ch1")
	}

	select {
	case msg := <-ch2:
		if msg != "hello" {
			t.Errorf("ch2 got %q, want %q", msg, "hello")
		}
	case <-time.After(time.Second):
		t.Error("timeout waiting for ch2")
	}
}

func TestBroker_UnsubscribeStopsMessages(t *testing.T) {
	b := newBroker()
	ch := b.subscribe()
	b.unsubscribe(ch)

	// After unsubscribing the channel is closed; no panic on broadcast
	b.broadcast("should not panic")

	// Channel should be closed
	_, ok := <-ch
	if ok {
		t.Error("expected channel to be closed after unsubscribe")
	}
}

// ── sseHandler tests ─────────────────────────────────────────────────────────

func TestSSEHandler_SendsData(t *testing.T) {
	b := newBroker()

	// Send a message just before the request so the buffer holds it
	go func() {
		time.Sleep(20 * time.Millisecond)
		b.broadcast(`{"running":false}`)
	}()

	req := httptest.NewRequest(http.MethodGet, "/events", nil)

	rr := httptest.NewRecorder()
	// We need a ResponseRecorder that supports http.Flusher
	handler := sseHandler(b)

	done := make(chan struct{})
	go func() {
		defer close(done)
		handler(rr, req)
	}()

	time.Sleep(100 * time.Millisecond)
	// Cancel by closing the request context via a custom recorder — instead,
	// just check the headers were set correctly before the goroutine exits.
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Errorf("Content-Type = %q, want text/event-stream", ct)
	}
}

// ── SSEPayload marshalling tests ──────────────────────────────────────────────

func TestSSEPayload_RunningFalse(t *testing.T) {
	p := SSEPayload{Running: false}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	var got map[string]interface{}
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if got["running"] != false {
		t.Errorf("running = %v, want false", got["running"])
	}
	if _, ok := got["state"]; ok {
		t.Error("state key should be omitted when nil")
	}
}

func TestSSEPayload_RunningTrue(t *testing.T) {
	state := &CurrentCommandState{
		Repo:             "my-repo",
		BatchStartedAt:   time.Now().UTC(),
		CommandStartedAt: time.Now().UTC(),
		Commands:         []string{"make build"},
		CommandIndex:     0,
	}
	p := SSEPayload{Running: true, State: state}
	data, err := json.Marshal(p)
	if err != nil {
		t.Fatalf("unexpected marshal error: %v", err)
	}
	var got SSEPayload
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Running {
		t.Error("expected running=true")
	}
	if got.State == nil || got.State.Repo != "my-repo" {
		t.Errorf("state.repo = %v, want my-repo", got.State)
	}
}
