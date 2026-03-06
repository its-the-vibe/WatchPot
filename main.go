package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
	"gopkg.in/yaml.v3"
)

// Config holds all application configuration loaded from the YAML config file.
type Config struct {
	RedisAddr    string `yaml:"redis_addr"`
	RedisChannel string `yaml:"redis_channel"`
	RedisKey     string `yaml:"redis_key"`
	ServerAddr   string `yaml:"server_addr"`
}

// CurrentCommandState mirrors the state payload written by Poppit.
type CurrentCommandState struct {
	Repo             string    `json:"repo"`
	BatchStartedAt   time.Time `json:"batch_started_at"`
	CommandStartedAt time.Time `json:"command_started_at"`
	Commands         []string  `json:"commands"`
	CommandIndex     int       `json:"command_index"`
}

// SSEPayload is the data sent to the frontend via Server-Sent Events.
type SSEPayload struct {
	Running bool                 `json:"running"`
	State   *CurrentCommandState `json:"state,omitempty"`
}

// broker manages SSE client connections and broadcasts events.
type broker struct {
	mu      sync.RWMutex
	clients map[chan string]struct{}
}

func newBroker() *broker {
	return &broker{clients: make(map[chan string]struct{})}
}

func (b *broker) subscribe() chan string {
	ch := make(chan string, 8)
	b.mu.Lock()
	b.clients[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *broker) unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.clients, ch)
	b.mu.Unlock()
	close(ch)
}

func (b *broker) broadcast(msg string) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for ch := range b.clients {
		select {
		case ch <- msg:
		default:
		}
	}
}

// loadConfig reads the YAML config file and overlays any env-var overrides.
func loadConfig(path string) (Config, error) {
	cfg := Config{
		RedisAddr:  "localhost:6379",
		ServerAddr: ":8080",
	}

	data, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return cfg, fmt.Errorf("reading config file: %w", err)
		}
		log.Printf("Config file %s not found, using defaults/env vars", path)
	} else {
		if err := yaml.Unmarshal(data, &cfg); err != nil {
			return cfg, fmt.Errorf("parsing config file: %w", err)
		}
	}

	// Env-var overrides (non-sensitive)
	if v := os.Getenv("WATCHPOT_REDIS_ADDR"); v != "" {
		cfg.RedisAddr = v
	}
	if v := os.Getenv("WATCHPOT_REDIS_CHANNEL"); v != "" {
		cfg.RedisChannel = v
	}
	if v := os.Getenv("WATCHPOT_REDIS_KEY"); v != "" {
		cfg.RedisKey = v
	}
	if v := os.Getenv("WATCHPOT_SERVER_ADDR"); v != "" {
		cfg.ServerAddr = v
	}

	return cfg, nil
}

// sseHandler streams Server-Sent Events to a connected browser client.
func sseHandler(b *broker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "streaming unsupported", http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")

		ch := b.subscribe()
		defer b.unsubscribe(ch)

		for {
			select {
			case <-r.Context().Done():
				return
			case msg, ok := <-ch:
				if !ok {
					return
				}
				fmt.Fprintf(w, "data: %s\n\n", msg)
				flusher.Flush()
			}
		}
	}
}

// fetchState reads the current command state from Redis. Returns nil state (no
// error) when the key does not exist, indicating no command is running.
func fetchState(ctx context.Context, rdb *redis.Client, key string) (*CurrentCommandState, error) {
	if key == "" {
		return nil, nil
	}

	val, err := rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("reading state key %s: %w", key, err)
	}

	var state CurrentCommandState
	if err := json.Unmarshal([]byte(val), &state); err != nil {
		return nil, fmt.Errorf("parsing state: %w", err)
	}
	return &state, nil
}

// redisListener subscribes to the configured Redis channel, reads the current
// state on each message, and broadcasts an SSE payload to all connected clients.
func redisListener(ctx context.Context, rdb *redis.Client, cfg Config, b *broker) {
	if cfg.RedisChannel == "" {
		log.Println("No redis_channel configured; SSE updates disabled")
		return
	}

	for {
		if err := runSubscription(ctx, rdb, cfg, b); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Printf("Redis subscription error: %v – retrying in 5s", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}
}

func runSubscription(ctx context.Context, rdb *redis.Client, cfg Config, b *broker) error {
	pubsub := rdb.Subscribe(ctx, cfg.RedisChannel)
	defer pubsub.Close()

	ch := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg, ok := <-ch:
			if !ok {
				return errors.New("subscription channel closed")
			}
			log.Printf("Received event on channel %s: %s", msg.Channel, msg.Payload)

			state, err := fetchState(ctx, rdb, cfg.RedisKey)
			if err != nil {
				log.Printf("Error fetching state: %v", err)
				continue
			}

			payload := SSEPayload{
				Running: state != nil,
				State:   state,
			}
			data, err := json.Marshal(payload)
			if err != nil {
				log.Printf("Error marshalling SSE payload: %v", err)
				continue
			}

			b.broadcast(string(data))
			log.Printf("Broadcast SSE: running=%v", payload.Running)
		}
	}
}

func main() {
	// Load .env (sensitive vars like REDIS_PASSWORD) – ignore if missing
	if err := godotenv.Load(); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("Warning: could not load .env: %v", err)
	}

	cfgPath := "config.yaml"
	if v := os.Getenv("WATCHPOT_CONFIG"); v != "" {
		cfgPath = v
	}

	cfg, err := loadConfig(cfgPath)
	if err != nil {
		log.Fatalf("Failed to load config: %v", err)
	}
	log.Printf("Config loaded: addr=%s channel=%s key=%s server=%s",
		cfg.RedisAddr, cfg.RedisChannel, cfg.RedisKey, cfg.ServerAddr)

	rdb := redis.NewClient(&redis.Options{
		Addr:     cfg.RedisAddr,
		Password: os.Getenv("REDIS_PASSWORD"),
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := rdb.Ping(ctx).Err(); err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}
	log.Println("Connected to Redis")

	b := newBroker()

	go redisListener(ctx, rdb, cfg, b)

	mux := http.NewServeMux()
	mux.HandleFunc("/events", sseHandler(b))
	mux.Handle("/", http.FileServer(http.Dir("static")))

	srv := &http.Server{
		Addr:    cfg.ServerAddr,
		Handler: mux,
	}

	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
		<-sigCh
		log.Println("Shutting down…")
		cancel()
		shutCtx, shutCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutCancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			log.Printf("HTTP shutdown error: %v", err)
		}
	}()

	log.Printf("Listening on %s", cfg.ServerAddr)
	if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("HTTP server error: %v", err)
	}
	log.Println("Stopped")
}
