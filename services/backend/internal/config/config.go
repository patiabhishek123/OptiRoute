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
	JWTSecret       []byte        // JWT_SECRET (required, >= 32 bytes)
	AccessTokenTTL  time.Duration // ACCESS_TOKEN_TTL, default 15m
	RefreshTokenTTL time.Duration // REFRESH_TOKEN_TTL, default 720h (30 days)
	AllowSignup     bool          // ALLOW_SIGNUP: public organization signup, default true
}

func Load() (Config, error) {
	cfg := Config{
		HTTPAddr:        getenv("API_ADDR", ":8080"),
		DatabaseURL:     os.Getenv("DATABASE_URL"),
		RedisURL:        os.Getenv("REDIS_URL"),
		ShutdownTimeout: 15 * time.Second,
		JWTSecret:       []byte(os.Getenv("JWT_SECRET")),
		AccessTokenTTL:  15 * time.Minute,
		RefreshTokenTTL: 30 * 24 * time.Hour,
		AllowSignup:     true,
	}
	var errs []error
	if cfg.DatabaseURL == "" {
		errs = append(errs, errors.New("DATABASE_URL is required"))
	}
	if cfg.RedisURL == "" {
		errs = append(errs, errors.New("REDIS_URL is required"))
	}
	if len(cfg.JWTSecret) < 32 {
		errs = append(errs, errors.New("JWT_SECRET is required and must be at least 32 bytes"))
	}
	for key, dst := range map[string]*time.Duration{
		"ACCESS_TOKEN_TTL": &cfg.AccessTokenTTL, "REFRESH_TOKEN_TTL": &cfg.RefreshTokenTTL,
	} {
		if v := os.Getenv(key); v != "" {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				errs = append(errs, fmt.Errorf("%s: want a positive duration like 15m", key))
			}
			*dst = d
		}
	}
	if v := os.Getenv("ALLOW_SIGNUP"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("ALLOW_SIGNUP: %w", err))
		}
		cfg.AllowSignup = b
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
