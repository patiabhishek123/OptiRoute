// Package server wires the HTTP routes of all modules into one handler (modular monolith, spec §6).
package server

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/fleet"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/geography"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/identity"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/orders"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

type Deps struct {
	Log             *slog.Logger
	DB              *pgxpool.Pool
	Redis           *redis.Client
	JWTSecret       []byte
	AccessTokenTTL  time.Duration
	RefreshTokenTTL time.Duration
	AllowSignup     bool
}

// New returns the root handler. Business routes live under /api/v1 (spec §12).
func New(d Deps) (http.Handler, error) {
	mux := http.NewServeMux()

	tokens, err := identity.NewTokens(d.JWTSecret, d.AccessTokenTTL)
	if err != nil {
		return nil, err
	}
	authn := func(h http.Handler) http.Handler { return auth.Authenticate(tokens, h) }
	identity.NewHandler(identity.NewService(d.DB, tokens, d.RefreshTokenTTL), d.Log, d.AllowSignup).
		Routes(mux, authn)
	depots := geography.NewService(d.DB)
	geography.NewHandler(depots, d.Log).Routes(mux, authn)
	fleet.NewHandler(d.DB, d.Log).Routes(mux, authn)
	orders.NewHandler(d.DB, depots, d.Log).Routes(mux, authn)

	// Liveness: the process is serving. Readiness: its dependencies answer.
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
		defer cancel()
		checks := map[string]string{"database": "ok", "redis": "ok"}
		status := http.StatusOK
		if err := d.DB.Ping(ctx); err != nil {
			checks["database"], status = err.Error(), http.StatusServiceUnavailable
		}
		if err := d.Redis.Ping(ctx).Err(); err != nil {
			checks["redis"], status = err.Error(), http.StatusServiceUnavailable
		}
		httpx.WriteJSON(w, status, checks)
	})

	return httpx.Middleware(d.Log, mux), nil
}
