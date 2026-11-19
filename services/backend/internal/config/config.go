// Package config loads the API's settings from the environment (12-factor).
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"
)

type Config struct {
	HTTPAddr        string        // API_ADDR, default ":8080"
	DatabaseURL     string        // DATABASE_URL (required)
	RedisURL        string        // REDIS_URL (required)
	LogLevel        slog.Level    // LOG_LEVEL: debug|info|warn|error
	MigrateOnStart  bool          // MIGRATE_ON_START: apply migrations before serving (dev)
	ShutdownTimeout time.Duration // SHUTDOWN_TIMEOUT, default 15s
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:        getenv("API_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		RedisURL:        os.Getenv("REDIS_URL"),
		ShutdownTimeout: 15 * time.Second,
	}
	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL is required"))
	}
	if err := cfg.LogLevel.UnmarshalText([]byte(getenv("LOG_LEVEL", "info"))); err != nil {
		errs = append(errs, fmt.Errorf("LOG_LEVEL: %w", err))
	}
	if v := os.Getenv("MIGRATE_ON_START"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("MIGRATE_ON_START: %w", err))
		}
		cfg.MigrateOnStart = b
	}
	if v := os.Getenv("SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("SHUTDOWN_TIMEOUT: %w", err))
		}
		cfg.ShutdownTimeout = d
	}
	return cfg, errors.Join(errs...)
}

func getenv(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
