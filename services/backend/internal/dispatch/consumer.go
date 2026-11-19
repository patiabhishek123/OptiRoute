package dispatch

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
)

// Streams written by the optimizer worker (docs/ARCHITECTURE.md §2).
const (
	EventsStream = "opti:job-events"
	EventsDLQ    = "opti:job-events:dlq"
	ConsumerGrp  = "api"
)

// Consumer turns job events into DRAFT plans. Like the Python worker it acks only after the
// database commit, reclaims messages left by crashed consumers, and dead-letters events that can
// never succeed so they do not block the stream.
type Consumer struct {
	Pool    *pgxpool.Pool
	Redis   *redis.Client
	Log     *slog.Logger
	Stream  string        // default EventsStream
	DLQ     string        // default EventsDLQ
	Name    string        // consumer name, default hostname-pid
	Block   time.Duration // XREADGROUP block, default 5s
	LeaseMs time.Duration // reclaim pending messages idle for longer, default 60s
	// MaxDeliveries dead-letters an event that has failed this many deliveries, so one bad event
	// can never block the stream (default 5).
	MaxDeliveries int64
	// MaterializeFunc defaults to Materialize; tests replace it to simulate failures.
	MaterializeFunc func(ctx context.Context, pool *pgxpool.Pool, jobID uuid.UUID) (uuid.UUID, bool, error)
}

func (c *Consumer) defaults() {
	if c.Stream == "" {
		c.Stream = EventsStream
	}
	if c.DLQ == "" {
		c.DLQ = EventsDLQ
	}
	if c.Name == "" {
		host, _ := os.Hostname()
		c.Name = host + "-" + time.Now().Format("150405.000")
	}
	if c.Block == 0 {
		c.Block = 5 * time.Second
	}
	if c.LeaseMs == 0 {
		c.LeaseMs = time.Minute
	}
	if c.MaxDeliveries == 0 {
		c.MaxDeliveries = 5
	}
	if c.MaterializeFunc == nil {
		c.MaterializeFunc = Materialize
	}
}

// EnsureGroup creates the consumer group, reading from the beginning so events published before
// the API first started are not lost.
func (c *Consumer) EnsureGroup(ctx context.Context) error {
	c.defaults()
	err := c.Redis.XGroupCreateMkStream(ctx, c.Stream, ConsumerGrp, "0").Err()
	if err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	return nil
}

func (c *Consumer) Run(ctx context.Context) {
	if err := c.EnsureGroup(ctx); err != nil {
		c.Log.Error("job-event consumer cannot start", "err", err)
		return
	}
	backoff := time.Second
	for ctx.Err() == nil {
		if _, err := c.RunOnce(ctx); err != nil && ctx.Err() == nil {
			c.Log.Warn("job-event consumer error; retrying", "err", err, "in", backoff)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(backoff*2, 30*time.Second)
			continue
		}
		backoff = time.Second
	}
}

// RunOnce handles at most a few messages (stale ones first) and returns how many it processed.
func (c *Consumer) RunOnce(ctx context.Context) (int, error) {
	c.defaults()
	msgs, _, err := c.Redis.XAutoClaim(ctx, &redis.XAutoClaimArgs{
		Stream: c.Stream, Group: ConsumerGrp, Consumer: c.Name, MinIdle: c.LeaseMs, Start: "0-0", Count: 10,
	}).Result()
	if err != nil {
		return 0, err
	}
	// Reclaimed messages have failed before: give up on ones that keep failing.
	fresh := msgs[:0]
	for _, m := range msgs {
		pending, err := c.Redis.XPendingExt(ctx, &redis.XPendingExtArgs{
			Stream: c.Stream, Group: ConsumerGrp, Start: m.ID, End: m.ID, Count: 1,
		}).Result()
		if err != nil {
			return 0, err
		}
		if len(pending) == 1 && pending[0].RetryCount > c.MaxDeliveries {
			if err := c.deadLetter(ctx, m, "still failing after "+strconv.FormatInt(c.MaxDeliveries, 10)+" deliveries"); err != nil {
				return 0, err
			}
			continue
		}
		fresh = append(fresh, m)
	}
	msgs = fresh
	if len(msgs) == 0 {
		streams, err := c.Redis.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: ConsumerGrp, Consumer: c.Name, Streams: []string{c.Stream, ">"}, Count: 10, Block: c.Block,
		}).Result()
		if errors.Is(err, redis.Nil) {
			return 0, nil
		} else if err != nil {
			return 0, err
		}
		msgs = streams[0].Messages
	}
	// Try every message: one failing event must not stop the healthy ones behind it. Failures stay
	// pending and are reclaimed later (and dead-lettered after MaxDeliveries).
	var errs []error
	for _, m := range msgs {
		if err := c.handle(ctx, m); err != nil {
			errs = append(errs, fmt.Errorf("event %s: %w", m.ID, err))
		}
	}
	return len(msgs), errors.Join(errs...)
}

func (c *Consumer) handle(ctx context.Context, m redis.XMessage) error {
	status, _ := m.Values["status"].(string)
	raw, _ := m.Values["job_id"].(string)
	jobID, perr := uuid.Parse(raw)
	switch {
	case perr != nil:
		return c.deadLetter(ctx, m, "job_id is not a UUID")
	case status != "COMPLETED":
		// FAILED/INFEASIBLE/TIME_LIMIT/CANCELLED: no plan to build; the job row tells the story.
	default:
		planID, created, err := c.MaterializeFunc(ctx, c.Pool, jobID)
		if errors.Is(err, ErrMalformedResult) || database.IsPermanentDataError(err) {
			// Retrying cannot fix a malformed result or a constraint violation (e.g. a job whose
			// organization or vehicles no longer exist): dead-letter it instead of blocking the stream.
			return c.deadLetter(ctx, m, err.Error())
		} else if err != nil {
			return err
		}
		if created {
			c.Log.Info("route plan created", "job_id", jobID, "plan_id", planID)
		}
	}
	return c.Redis.XAck(ctx, c.Stream, ConsumerGrp, m.ID).Err()
}

func (c *Consumer) deadLetter(ctx context.Context, m redis.XMessage, reason string) error {
	c.Log.Error("job event dead-lettered", "id", m.ID, "reason", reason)
	values := map[string]any{"reason": reason, "event_id": m.ID}
	for k, v := range m.Values {
		values[k] = v
	}
	if err := c.Redis.XAdd(ctx, &redis.XAddArgs{Stream: c.DLQ, Values: values}).Err(); err != nil {
		return err
	}
	return c.Redis.XAck(ctx, c.Stream, ConsumerGrp, m.ID).Err()
}
