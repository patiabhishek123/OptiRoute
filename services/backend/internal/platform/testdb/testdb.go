// Package testdb gives each test package a fresh, fully migrated PostgreSQL database.
//
// It connects with TEST_DATABASE_URL (default: the dev database from docker-compose), creates
// optiroute_test_<random>, applies the embedded migrations, and drops it during cleanup.
// Locally the tests are skipped if Postgres is not reachable; in CI (CI=true) they fail instead.
package testdb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
)

const defaultURL = "postgres://optiroute:optiroute@localhost:15432/optiroute?sslmode=disable"

// New returns a pool connected to a new migrated database, plus that database's URL.
func New(t testing.TB) (*pgxpool.Pool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	adminURL := os.Getenv("TEST_DATABASE_URL")
	if adminURL == "" {
		adminURL = defaultURL
	}
	admin, err := pgx.Connect(ctx, adminURL)
	if err != nil {
		if os.Getenv("CI") != "" {
			t.Fatalf("test database unreachable: %v", err)
		}
		t.Skipf("Postgres not reachable (%v); run `make up`", err)
	}

	suffix := make([]byte, 6)
	_, _ = rand.Read(suffix)
	name := "optiroute_test_" + hex.EncodeToString(suffix)
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatalf("create test database: %v", err)
	}

	u, err := url.Parse(adminURL)
	if err != nil {
		t.Fatalf("parse TEST_DATABASE_URL: %v", err)
	}
	u.Path = "/" + name
	dbURL := u.String()

	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_, _ = admin.Exec(ctx, "DROP DATABASE IF EXISTS "+name+" WITH (FORCE)")
		_ = admin.Close(ctx)
	})

	if err := database.Migrate(ctx, dbURL, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("migrate test database: %v", err)
	}
	pool, err := database.Open(ctx, dbURL)
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool, dbURL
}
