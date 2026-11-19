// Package outbox relays rows from core.outbox to Redis Streams (transactional outbox pattern).
//
// Business code inserts an outbox row in the same transaction as its state change, so an event
// exists if and only if the change committed. The relay publishes unpublished rows in id order
// and marks them published. Delivery is at-least-once: if the process dies between XADD and the
// commit, the row is published again, and consumers must be idempotent (the optimizer worker
// is). FOR UPDATE SKIP LOCKED lets several API instances relay without double work.
package outbox

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
)

type Relay struct {
	Pool     *pgxpool.Pool
	Redis    *redis.Client
	Log      *slog.Logger
	Interval time.Duration // poll interval when idle (default 500ms)
	Batch    int           // rows per transaction (default 100)
}

// Run publishes until ctx is cancelled.
func (r *Relay) Run(ctx context.Context) {
	interval := r.Interval
	if interval == 0 {
		interval = 500 * time.Millisecond
	}
	for {
		n, err := r.PublishOnce(ctx)
		if err != nil && ctx.Err() == nil {
			r.Log.Warn("outbox relay failed; retrying", "err", err)
		}
		if n > 0 && err == nil {
			continue // drain backlog without waiting
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(interval):
		}
	}
}

// PublishOnce relays one batch and returns how many rows it published.
func (r *Relay) PublishOnce(ctx context.Context) (int, error) {
	batch := r.Batch
	if batch == 0 {
		batch = 100
	}
	tx, err := r.Pool.Begin(ctx)
	if err != nil {
		return 0, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	rows, err := tx.Query(ctx, `SELECT id, topic, payload FROM core.outbox
		WHERE published_at IS NULL ORDER BY id LIMIT $1 FOR UPDATE SKIP LOCKED`, batch)
	if err != nil {
		return 0, err
	}
	type event struct {
		id      int64
		topic   string
		payload []byte
	}
	events, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (event, error) {
		var e event
		err := row.Scan(&e.id, &e.topic, &e.payload)
		return e, err
	})
	if err != nil || len(events) == 0 {
		return 0, err
	}

	ids := make([]int64, len(events))
	for i, e := range events {
		var fields map[string]any
		if err := json.Unmarshal(e.payload, &fields); err != nil {
			return 0, fmt.Errorf("outbox %d: payload must be a JSON object: %w", e.id, err)
		}
		if err := r.Redis.XAdd(ctx, &redis.XAddArgs{Stream: e.topic, Values: fields}).Err(); err != nil {
			return 0, fmt.Errorf("outbox %d: publish to %s: %w", e.id, e.topic, err)
		}
		ids[i] = e.id
	}
	if _, err := tx.Exec(ctx, `UPDATE core.outbox SET published_at = now() WHERE id = ANY($1)`, ids); err != nil {
		return 0, err
	}
	return len(events), tx.Commit(ctx)
}
