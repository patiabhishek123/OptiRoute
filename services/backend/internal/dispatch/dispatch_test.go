package dispatch_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/dispatch"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
)

type fixture struct {
	t                 *testing.T
	pool              *pgxpool.Pool
	org, orgB         uuid.UUID
	vehicles, drivers []uuid.UUID
	orders            []uuid.UUID // 3 orders on 2026-11-02
	ctx               context.Context
	resultFor         func(unassigned bool) map[string]any
	insertJob         func(status string, result any) uuid.UUID
	orderStatus       func(id uuid.UUID) string
	count             func(sql string, args ...any) int
}

func setup(t *testing.T) *fixture {
	pool, _ := testdb.New(t)
	f := &fixture{t: t, pool: pool, ctx: context.Background()}
	f.org, f.orgB = testdb.CreateOrg(t, pool, "org-a"), testdb.CreateOrg(t, pool, "org-b")
	id := func(sql string, args ...any) uuid.UUID {
		var v uuid.UUID
		if err := pool.QueryRow(f.ctx, sql, args...).Scan(&v); err != nil {
			t.Fatalf("%s: %v", sql, err)
		}
		return v
	}
	depot := id(`INSERT INTO core.depots (organization_id, name, location) VALUES ($1, 'BBSR', 'SRID=4326;POINT(85.82 20.29)') RETURNING id`, f.org)
	for i := range 2 {
		f.vehicles = append(f.vehicles, id(`INSERT INTO operations.vehicles (organization_id, depot_id, registration_number,
			vehicle_type, capacity_weight_kg, capacity_volume_m3, cost_per_km) VALUES ($1, $2, $3, 'van', 500, 4, 12) RETURNING id`,
			f.org, depot, fmt.Sprintf("V%d", i+1)))
		f.drivers = append(f.drivers, id(`INSERT INTO operations.drivers (organization_id, name, shift_start, shift_end)
			VALUES ($1, $2, '08:00', '18:00') RETURNING id`, f.org, fmt.Sprintf("Driver %d", i+1)))
	}
	for i := range 3 {
		f.orders = append(f.orders, id(`INSERT INTO operations.orders (organization_id, external_order_id, planning_date,
			customer_name, delivery_location, time_window_start, time_window_end)
			VALUES ($1, $2, '2026-11-02', 'C', 'SRID=4326;POINT(85.83 20.30)', '2026-11-02T09:00+05:30', '2026-11-02T13:00+05:30') RETURNING id`,
			f.org, fmt.Sprintf("O-%d", i+1)))
	}

	// A FEASIBLE SolveResult: V1 serves O-1 then O-2 (09:10 and 09:40 local); V2 serves O-3.
	f.resultFor = func(unassigned bool) map[string]any {
		r := map[string]any{
			"status":    "FEASIBLE",
			"objective": map[string]any{"total": 1234.5, "travel": 1000.0},
			"kpis":      map[string]any{"distance_m": 42_500, "duration_s": 9000, "vehicles_used": 2, "late_stops": 0},
			"routes": []any{
				map[string]any{"vehicle_id": f.vehicles[0], "driver_id": f.drivers[0], "distance_m": 30_000, "duration_s": 5401,
					"stops": []any{
						map[string]any{"order_id": f.orders[0], "seq": 1, "arrival_s": 33000, "departure_s": 33300},
						map[string]any{"order_id": f.orders[1], "seq": 2, "arrival_s": 34800, "departure_s": 35100},
					}},
				map[string]any{"vehicle_id": f.vehicles[1], "driver_id": f.drivers[1], "distance_m": 12_500, "duration_s": 3600,
					"stops": []any{map[string]any{"order_id": f.orders[2], "seq": 1, "arrival_s": 36000, "departure_s": 36300}}},
			},
			"unassigned": []any{},
			"meta":       map[string]any{"algorithm": "ORTOOLS_GLS"},
		}
		if unassigned {
			routes := r["routes"].([]any)
			r["routes"] = routes[:1]
			r["unassigned"] = []any{map[string]any{"order_id": f.orders[2], "reason": "NOT_ROUTED"}}
		}
		return r
	}
	f.insertJob = func(status string, result any) uuid.UUID {
		res, _ := json.Marshal(result)
		if result == nil {
			res = nil
		}
		return id(`INSERT INTO optimization.jobs (organization_id, status, planning_date, input_snapshot, result)
			VALUES ($1, $2, '2026-11-02', '{"timezone": "Asia/Kolkata"}', $3) RETURNING id`, f.org, status, res)
	}
	f.orderStatus = func(oid uuid.UUID) string {
		var s string
		_ = pool.QueryRow(f.ctx, "SELECT status FROM operations.orders WHERE id = $1", oid).Scan(&s)
		return s
	}
	f.count = func(sql string, args ...any) int {
		var n int
		_ = pool.QueryRow(f.ctx, sql, args...).Scan(&n)
		return n
	}
	return f
}

func TestMaterializeIsIdempotentAndVersioned(t *testing.T) {
	f := setup(t)
	job := f.insertJob("COMPLETED", f.resultFor(false))

	planID, created, err := dispatch.Materialize(f.ctx, f.pool, job)
	if err != nil || !created || planID == uuid.Nil {
		t.Fatalf("materialize = %v %v %v", planID, created, err)
	}
	again, created, err := dispatch.Materialize(f.ctx, f.pool, job)
	if err != nil || created || again != planID {
		t.Fatalf("second materialize = %v %v %v, want same plan, not created", again, created, err)
	}
	if n := f.count("SELECT count(*) FROM routing.route_plans"); n != 1 {
		t.Fatalf("%d plans after redelivery", n)
	}
	if n := f.count("SELECT count(*) FROM routing.route_stops"); n != 3 {
		t.Fatalf("%d stops", n)
	}
	for _, o := range f.orders {
		if s := f.orderStatus(o); s != "PENDING" {
			t.Errorf("a draft plan changed order state to %s", s)
		}
	}

	second, _, err := dispatch.Materialize(f.ctx, f.pool, f.insertJob("COMPLETED", f.resultFor(true)))
	if err != nil {
		t.Fatal(err)
	}
	var version, unassigned int
	_ = f.pool.QueryRow(f.ctx, "SELECT version, cardinality(unassigned_order_ids) FROM routing.route_plans WHERE id = $1", second).Scan(&version, &unassigned)
	if version != 2 || unassigned != 1 {
		t.Errorf("second plan version=%d unassigned=%d, want 2/1", version, unassigned)
	}

	if id, created, err := dispatch.Materialize(f.ctx, f.pool, f.insertJob("FAILED", nil)); err != nil || created || id != uuid.Nil {
		t.Errorf("FAILED job = %v %v %v, want skipped", id, created, err)
	}
	if _, _, err := dispatch.Materialize(f.ctx, f.pool, f.insertJob("COMPLETED", map[string]any{"status": "FEASIBLE"})); !errors.Is(err, dispatch.ErrMalformedResult) {
		t.Errorf("malformed result err = %v", err)
	}
}

func TestConsumerAcksDeadLettersAndMaterializes(t *testing.T) {
	f := setup(t)
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:16379/15"
	}
	opts, _ := redis.ParseURL(redisURL)
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	tag := uuid.NewString()[:8]
	c := &dispatch.Consumer{Pool: f.pool, Redis: rdb, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Stream: "test:" + tag + ":events", DLQ: "test:" + tag + ":dlq", Name: "t", Block: 100 * time.Millisecond}
	defer rdb.Del(f.ctx, c.Stream, c.DLQ)
	if err := c.EnsureGroup(f.ctx); err != nil {
		t.Fatal(err)
	}

	good := f.insertJob("COMPLETED", f.resultFor(false))
	events := []map[string]any{
		{"job_id": good.String(), "status": "COMPLETED"},
		{"job_id": f.insertJob("FAILED", nil).String(), "status": "FAILED"},
		{"job_id": "not-a-uuid", "status": "COMPLETED"},
		{"job_id": f.insertJob("COMPLETED", map[string]any{"status": "FEASIBLE"}).String(), "status": "COMPLETED"},
		{"job_id": good.String(), "status": "COMPLETED"}, // duplicate delivery
	}
	for _, e := range events {
		rdb.XAdd(f.ctx, &redis.XAddArgs{Stream: c.Stream, Values: e})
	}
	if n, err := c.RunOnce(f.ctx); err != nil || n != len(events) {
		t.Fatalf("RunOnce = %d %v", n, err)
	}
	if n := f.count("SELECT count(*) FROM routing.route_plans WHERE optimization_job_id = $1", good); n != 1 {
		t.Errorf("plans for good job = %d", n)
	}
	if n := rdb.XLen(f.ctx, c.DLQ).Val(); n != 2 {
		t.Errorf("dead letters = %d, want 2", n)
	}
	if p := rdb.XPending(f.ctx, c.Stream, dispatch.ConsumerGrp).Val(); p.Count != 0 {
		t.Errorf("%d events left pending", p.Count)
	}
}

func TestPlanReadAPI(t *testing.T) {
	f := setup(t)
	planID, _, err := dispatch.Materialize(f.ctx, f.pool, f.insertJob("COMPLETED", f.resultFor(false)))
	if err != nil {
		t.Fatal(err)
	}
	v := authtest.Verifier{
		"a": {UserID: uuid.New(), OrgID: f.org, Role: auth.RoleViewer},
		"b": {UserID: uuid.New(), OrgID: f.orgB, Role: auth.RoleAdmin},
	}
	mux := http.NewServeMux()
	dispatch.NewHandler(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	defer srv.Close()
	get := func(path, token string, out any) int {
		req, _ := http.NewRequest("GET", srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}

	var list struct{ Items []dispatch.PlanSummary }
	if code := get("/api/v1/route-plans?planning_date=2026-11-02&status=draft", "a", &list); code != 200 || len(list.Items) != 1 {
		t.Fatalf("list = %d %+v", code, list)
	}
	if s := list.Items[0]; s.RouteCount != 2 || s.TotalDistanceKm != 42.5 || s.TotalCost != 1234.5 || s.Algorithm != "ORTOOLS_GLS" || s.Version != 1 {
		t.Errorf("summary %+v", s)
	}

	var plan dispatch.Plan
	if code := get("/api/v1/route-plans/"+planID.String(), "a", &plan); code != 200 {
		t.Fatalf("get plan = %d", code)
	}
	r1 := plan.Routes[0]
	if r1.RegistrationNumber != "V1" || *r1.DriverName != "Driver 1" || r1.EstimatedDurationMinutes != 91 || len(r1.Stops) != 2 {
		t.Fatalf("route 1 %+v", r1)
	}
	ist := time.FixedZone("IST", 5*3600+1800)
	if want := time.Date(2026, 11, 2, 9, 10, 0, 0, ist); !r1.Stops[0].PlannedArrival.Equal(want) || r1.Stops[1].ExternalOrderID != "O-2" {
		t.Errorf("stop 1 arrival %v, want %v; stops %+v", r1.Stops[0].PlannedArrival, want, r1.Stops)
	}

	var stops struct{ Items []dispatch.Stop }
	if code := get("/api/v1/routes/"+r1.ID.String()+"/stops", "a", &stops); code != 200 || len(stops.Items) != 2 || stops.Items[1].Sequence != 2 {
		t.Errorf("route stops = %d %+v", code, stops)
	}
	for _, p := range []string{"/api/v1/route-plans/" + planID.String(), "/api/v1/routes/" + r1.ID.String()} {
		if code := get(p, "b", nil); code != 404 {
			t.Errorf("other org %s = %d, want 404", p, code)
		}
	}
}

func newTestConsumer(t *testing.T, f *fixture) (*dispatch.Consumer, *redis.Client) {
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:16379/15"
	}
	opts, _ := redis.ParseURL(redisURL)
	rdb := redis.NewClient(opts)
	tag := uuid.NewString()[:8]
	c := &dispatch.Consumer{Pool: f.pool, Redis: rdb, Log: slog.New(slog.NewTextHandler(io.Discard, nil)),
		Stream: "test:" + tag + ":events", DLQ: "test:" + tag + ":dlq", Name: "t", Block: 50 * time.Millisecond}
	t.Cleanup(func() { rdb.Del(f.ctx, c.Stream, c.DLQ); rdb.Close() })
	if err := c.EnsureGroup(f.ctx); err != nil {
		t.Fatal(err)
	}
	return c, rdb
}

// Regression: a job whose result references another organization's vehicle violates the tenant
// foreign key. That can never succeed, so it must be dead-lettered, not retried forever.
func TestConstraintViolationIsDeadLetteredNotRetried(t *testing.T) {
	f := setup(t)
	c, rdb := newTestConsumer(t, f)
	var foreign uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `WITH d AS (INSERT INTO core.depots (organization_id, name, location)
		VALUES ($1, 'B', 'SRID=4326;POINT(85 20)') RETURNING id)
		INSERT INTO operations.vehicles (organization_id, depot_id, registration_number, vehicle_type,
		  capacity_weight_kg, capacity_volume_m3, cost_per_km) SELECT $1, id, 'B1', 'van', 1, 1, 1 FROM d RETURNING id`,
		f.orgB).Scan(&foreign); err != nil {
		t.Fatal(err)
	}
	result := f.resultFor(false)
	result["routes"].([]any)[0].(map[string]any)["vehicle_id"] = foreign
	job := f.insertJob("COMPLETED", result)
	rdb.XAdd(f.ctx, &redis.XAddArgs{Stream: c.Stream, Values: map[string]any{"job_id": job.String(), "status": "COMPLETED"}})

	if _, err := c.RunOnce(f.ctx); err != nil {
		t.Fatalf("RunOnce returned %v; a permanent error must not be retried", err)
	}
	if n := rdb.XLen(f.ctx, c.DLQ).Val(); n != 1 {
		t.Errorf("dead letters = %d, want 1", n)
	}
	if n := f.count("SELECT count(*) FROM routing.route_plans"); n != 0 {
		t.Errorf("a partial plan was committed (%d)", n)
	}
}

// Regression: an event that fails transiently forever is dead-lettered after MaxDeliveries, and the
// events behind it still get processed.
func TestRepeatedFailureCannotBlockTheStream(t *testing.T) {
	f := setup(t)
	c, rdb := newTestConsumer(t, f)
	c.LeaseMs, c.MaxDeliveries = time.Millisecond, 3
	bad := uuid.New()
	c.MaterializeFunc = func(ctx context.Context, pool *pgxpool.Pool, job uuid.UUID) (uuid.UUID, bool, error) {
		if job == bad {
			return uuid.Nil, false, errors.New("database briefly unavailable")
		}
		return dispatch.Materialize(ctx, pool, job)
	}
	good := f.insertJob("COMPLETED", f.resultFor(false))
	rdb.XAdd(f.ctx, &redis.XAddArgs{Stream: c.Stream, Values: map[string]any{"job_id": bad.String(), "status": "COMPLETED"}})
	rdb.XAdd(f.ctx, &redis.XAddArgs{Stream: c.Stream, Values: map[string]any{"job_id": good.String(), "status": "COMPLETED"}})

	for range 10 {
		_, _ = c.RunOnce(f.ctx)
		time.Sleep(5 * time.Millisecond)
	}
	if n := f.count("SELECT count(*) FROM routing.route_plans WHERE optimization_job_id = $1", good); n != 1 {
		t.Errorf("event behind the failing one was not processed (plans=%d)", n)
	}
	if n := rdb.XLen(f.ctx, c.DLQ).Val(); n != 1 {
		t.Errorf("dead letters = %d, want 1", n)
	}
	if p := rdb.XPending(f.ctx, c.Stream, dispatch.ConsumerGrp).Val(); p.Count != 0 {
		t.Errorf("%d events still pending", p.Count)
	}
}
