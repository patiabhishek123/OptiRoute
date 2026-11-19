// Command api is the OptiRoute business API (Go modular monolith).
//
//	api serve     # default: serve HTTP (MIGRATE_ON_START=true also migrates first)
//	api migrate   # apply pending database migrations and exit
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/config"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/outbox"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/server"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.LogLevel}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "migrate":
		return database.Migrate(ctx, cfg.DatabaseURL, log)
	case "serve":
		return serve(ctx, cfg, log)
	default:
		return fmt.Errorf("unknown command %q (want serve or migrate)", cmd)
	}
}

func serve(ctx context.Context, cfg config.Config, log *slog.Logger) error {
	if cfg.MigrateOnStart {
		if err := database.Migrate(ctx, cfg.DatabaseURL, log); err != nil {
			return err
		}
	}
	pool, err := database.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		return err
	}
	defer pool.Close()

	ropts, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return fmt.Errorf("REDIS_URL: %w", err)
	}
	rdb := redis.NewClient(ropts)
	defer rdb.Close()

	handler, err := server.New(server.Deps{
		Log: log, DB: pool, Redis: rdb, JWTSecret: cfg.JWTSecret,
		AccessTokenTTL: cfg.AccessTokenTTL, RefreshTokenTTL: cfg.RefreshTokenTTL,
		AllowSignup: cfg.AllowSignup,
	})
	if err != nil {
		return err
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           handler,
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}
	// Relay outbox events (e.g. new optimization jobs) to Redis for as long as the server runs.
	relay := &outbox.Relay{Pool: pool, Redis: rdb, Log: log}
	relayCtx, stopRelay := context.WithCancel(ctx)
	defer stopRelay()
	go relay.Run(relayCtx)

	errc := make(chan error, 1)
	go func() {
		log.Info("api listening", "addr", cfg.HTTPAddr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("graceful shutdown: %w", err)
		}
	}
	return nil
}
