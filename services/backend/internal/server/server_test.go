package server_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/redis/go-redis/v9"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/server"
)

func TestHealthAndReadiness(t *testing.T) {
	pool, _ := testdb.New(t)
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:16379/15"
	}
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("ready when dependencies answer", func(t *testing.T) {
		rdb := redis.NewClient(opts)
		defer rdb.Close()
		srv := httptest.NewServer(server.New(server.Deps{Log: log, DB: pool, Redis: rdb}))
		defer srv.Close()

		for path, want := range map[string]int{"/healthz": 200, "/readyz": 200} {
			resp, err := http.Get(srv.URL + path)
			if err != nil {
				t.Fatal(err)
			}
			resp.Body.Close()
			if resp.StatusCode != want {
				t.Errorf("%s = %d, want %d", path, resp.StatusCode, want)
			}
			if resp.Header.Get("X-Request-ID") == "" {
				t.Errorf("%s: missing X-Request-ID", path)
			}
		}
	})

	t.Run("not ready when redis is down", func(t *testing.T) {
		rdb := redis.NewClient(&redis.Options{Addr: "127.0.0.1:9", MaxRetries: -1})
		defer rdb.Close()
		srv := httptest.NewServer(server.New(server.Deps{Log: log, DB: pool, Redis: rdb}))
		defer srv.Close()

		resp, err := http.Get(srv.URL + "/readyz")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body map[string]string
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusServiceUnavailable || body["database"] != "ok" || body["redis"] == "ok" {
			t.Errorf("got %d %v, want 503 with database ok and redis failing", resp.StatusCode, body)
		}
	})
}
