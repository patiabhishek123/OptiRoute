package planning_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"github.com/santhosh-tekuri/jsonschema/v6"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/planning"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/outbox"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
)

type env struct {
	t    *testing.T
	url  string
	pool *pgxpool.Pool
	org  uuid.UUID
}

func (e env) exec(sql string, args ...any) {
	e.t.Helper()
	if _, err := e.pool.Exec(context.Background(), sql, args...); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
}

func (e env) id(sql string, args ...any) uuid.UUID {
	e.t.Helper()
	var id uuid.UUID
	if err := e.pool.QueryRow(context.Background(), sql, args...).Scan(&id); err != nil {
		e.t.Fatalf("%s: %v", sql, err)
	}
	return id
}

// setup seeds org A with a depot, two paired vehicles (one 08:00-18:00 with a 30 min break), an
// unpaired vehicle, a paired vehicle under maintenance, and four orders on 2026-11-02 (one
// cancelled) plus one on another day.
func setup(t *testing.T) env {
	pool, _ := testdb.New(t)
	e := env{t: t, pool: pool, org: testdb.CreateOrg(t, pool, "org-a")}
	orgB := testdb.CreateOrg(t, pool, "org-b")

	depot := e.id(`INSERT INTO core.depots (organization_id, name, location)
		VALUES ($1, 'BBSR', 'SRID=4326;POINT(85.8245 20.2961)') RETURNING id`, e.org)
	vehicle := func(reg, status string) uuid.UUID {
		return e.id(`INSERT INTO operations.vehicles (organization_id, depot_id, registration_number, vehicle_type,
			capacity_weight_kg, capacity_volume_m3, cost_per_km, fixed_cost, status)
			VALUES ($1, $2, $3, 'van', 500, 4.5, 12, 800, $4) RETURNING id`, e.org, depot, reg, status)
	}
	driver := func(name string) uuid.UUID {
		return e.id(`INSERT INTO operations.drivers (organization_id, name, shift_start, shift_end,
			break_duration_minutes, max_overtime_minutes)
			VALUES ($1, $2, '08:00', '18:00', 30, 60) RETURNING id`, e.org, name)
	}
	pair := func(v, d uuid.UUID) {
		e.exec(`INSERT INTO operations.driver_assignments (organization_id, planning_date, vehicle_id, driver_id)
			VALUES ($1, '2026-11-02', $2, $3)`, e.org, v, d)
	}
	pair(vehicle("V1", "AVAILABLE"), driver("D1"))
	pair(vehicle("V2", "AVAILABLE"), driver("D2"))
	vehicle("V3-unpaired", "AVAILABLE")
	pair(vehicle("V4-maint", "MAINTENANCE"), driver("D4"))

	order := func(ext, date, status string, depotID any) {
		e.exec(`INSERT INTO operations.orders (organization_id, depot_id, external_order_id, planning_date,
			customer_name, delivery_location, demand_weight_kg, demand_volume_m3, time_window_start,
			time_window_end, service_duration_minutes, priority, status)
			VALUES ($1, $2, $3, $4, 'C', 'SRID=4326;POINT(85.83 20.30)', 12.5, 0.05,
			        ($4::date + time '09:00') AT TIME ZONE 'Asia/Kolkata',
			        ($4::date + time '13:00') AT TIME ZONE 'Asia/Kolkata', 6, 2, $5)`,
			e.org, depotID, ext, date, status)
	}
	order("O-1", "2026-11-02", "PENDING", depot)
	order("O-2", "2026-11-02", "PENDING", nil)
	order("O-3", "2026-11-02", "PENDING", nil)
	order("O-4", "2026-11-02", "CANCELLED", nil)
	order("O-5", "2026-11-03", "PENDING", nil)

	v := authtest.Verifier{
		"a":      {UserID: uuid.New(), OrgID: e.org, Role: auth.RoleDispatcher},
		"viewer": {UserID: uuid.New(), OrgID: e.org, Role: auth.RoleViewer},
		"b":      {UserID: uuid.New(), OrgID: orgB, Role: auth.RoleAdmin},
	}
	mux := http.NewServeMux()
	planning.NewHandler(pool, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	e.url = srv.URL
	return e
}

func (e env) call(method, path, token, idemKey string, body, out any) int {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.url+path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	if idemKey != "" {
		req.Header.Set("Idempotency-Key", idemKey)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

var jobBody = map[string]any{"planning_date": "2026-11-02", "solver": map[string]any{"time_limit_seconds": 10}}

func schema(t *testing.T) *jsonschema.Schema {
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "../../../../packages/api-contracts/optimizer/v1/solve-request.schema.json")
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	doc, err := jsonschema.UnmarshalJSON(f)
	if err != nil {
		t.Fatal(err)
	}
	c := jsonschema.NewCompiler()
	if err := c.AddResource("solve-request.json", doc); err != nil {
		t.Fatal(err)
	}
	s, err := c.Compile("solve-request.json")
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestCreateJobSnapshotMatchesContract(t *testing.T) {
	e := setup(t)
	var job planning.Job
	if code := e.call("POST", "/api/v1/optimization/jobs", "a", "", jobBody, &job); code != 202 {
		t.Fatalf("create = %d", code)
	}
	if job.Status != "QUEUED" || job.OrderCount != 3 || job.VehicleCount != 2 || job.Algorithm != "ORTOOLS_GLS" {
		t.Fatalf("job %+v", job)
	}

	var raw []byte
	if err := e.pool.QueryRow(context.Background(), "SELECT input_snapshot FROM optimization.jobs WHERE id = $1", job.ID).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var doc any
	_ = json.Unmarshal(raw, &doc)
	if err := schema(t).Validate(doc); err != nil {
		t.Fatalf("snapshot violates the optimizer contract:\n%v", err)
	}

	var snap planning.SolveRequest
	_ = json.Unmarshal(raw, &snap)
	if snap.JobID != job.ID.String() || snap.Timezone != "Asia/Kolkata" || len(snap.Depots) != 1 || snap.Solver.TimeWindows != "SOFT" {
		t.Fatalf("snapshot header %+v", snap)
	}
	v := snap.Vehicles[0]
	if v.ShiftStartS != 8*3600 || v.ShiftEndS != 18*3600 || v.BreakS != 1800 || v.MaxOvertimeS != 3600 ||
		v.CapWeightG != 500_000 || v.CapVolumeCm3 != 4_500_000 || v.DriverID == "" {
		t.Errorf("vehicle units %+v", v)
	}
	o := snap.Orders[0]
	if o.TwStartS != 9*3600 || o.TwEndS != 13*3600 || o.WeightG != 12_500 || o.VolumeCm3 != 50_000 ||
		o.ServiceS != 360 || o.Priority != 2 || o.DepotID == nil || snap.Orders[1].DepotID != nil {
		t.Errorf("order units %+v", o)
	}
}

func TestIdempotencyAndValidation(t *testing.T) {
	e := setup(t)
	var first, again planning.Job
	if code := e.call("POST", "/api/v1/optimization/jobs", "a", "plan-2026-11-02", jobBody, &first); code != 202 {
		t.Fatalf("first = %d", code)
	}
	if code := e.call("POST", "/api/v1/optimization/jobs", "a", "plan-2026-11-02", jobBody, &again); code != 200 || again.ID != first.ID {
		t.Fatalf("retry = %d %v, want 200 with %v", code, again.ID, first.ID)
	}
	var jobs, events int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM optimization.jobs").Scan(&jobs)
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM core.outbox").Scan(&events)
	if jobs != 1 || events != 1 {
		t.Fatalf("jobs=%d outbox=%d, want 1/1", jobs, events)
	}

	cases := []struct {
		name  string
		body  map[string]any
		token string
		want  int
		code  string
	}{
		{"no pairing that day", map[string]any{"planning_date": "2026-11-03"}, "a", 422, "NO_VEHICLES"},
		{"no orders for filter", map[string]any{"planning_date": "2026-11-02", "batch_id": "nope"}, "a", 422, "NO_ORDERS"},
		{"bad algorithm", map[string]any{"planning_date": "2026-11-02", "solver": map[string]any{"algorithm": "MAGIC"}}, "a", 422, "VALIDATION_FAILED"},
		{"viewer cannot plan", jobBody, "viewer", 403, "FORBIDDEN"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var resp struct{ Error struct{ Code string } }
			if got := e.call("POST", "/api/v1/optimization/jobs", c.token, "", c.body, &resp); got != c.want || resp.Error.Code != c.code {
				t.Errorf("got %d %s, want %d %s", got, resp.Error.Code, c.want, c.code)
			}
		})
	}
}

func TestJobLifecycleEndpoints(t *testing.T) {
	e := setup(t)
	var job planning.Job
	e.call("POST", "/api/v1/optimization/jobs", "a", "", jobBody, &job)
	path := "/api/v1/optimization/jobs/" + job.ID.String()

	if code := e.call("GET", path, "b", "", nil, nil); code != 404 {
		t.Errorf("other org = %d, want 404", code)
	}
	var resp struct{ Error struct{ Code string } }
	if code := e.call("GET", path+"/result", "a", "", nil, &resp); code != 404 || resp.Error.Code != "RESULT_NOT_READY" {
		t.Errorf("early result = %d %+v", code, resp)
	}
	e.exec(`UPDATE optimization.jobs SET status='COMPLETED', result='{"status":"FEASIBLE"}' WHERE id=$1`, job.ID)
	var result map[string]any
	if code := e.call("GET", path+"/result", "a", "", nil, &result); code != 200 || result["status"] != "FEASIBLE" {
		t.Errorf("result = %d %+v", code, result)
	}
	if code := e.call("POST", path+"/cancel", "a", "", nil, nil); code != 409 {
		t.Errorf("cancel finished job = %d, want 409", code)
	}

	var queued, cancelled planning.Job
	e.call("POST", "/api/v1/optimization/jobs", "a", "", jobBody, &queued)
	if code := e.call("POST", "/api/v1/optimization/jobs/"+queued.ID.String()+"/cancel", "a", "", nil, &cancelled); code != 202 ||
		cancelled.Status != "CANCELLED" || !cancelled.CancelRequested || cancelled.CompletedAt == nil {
		t.Errorf("cancel queued = %d %+v", code, cancelled)
	}
	var list struct{ Items []planning.Job }
	e.call("GET", "/api/v1/optimization/jobs?planning_date=2026-11-02", "viewer", "", nil, &list)
	if len(list.Items) != 2 || list.Items[0].ID != queued.ID {
		t.Errorf("list (newest first) = %+v", list.Items)
	}
}

func TestOutboxRelayPublishesEachEventOnce(t *testing.T) {
	e := setup(t)
	redisURL := os.Getenv("TEST_REDIS_URL")
	if redisURL == "" {
		redisURL = "redis://localhost:16379/15"
	}
	opts, _ := redis.ParseURL(redisURL)
	rdb := redis.NewClient(opts)
	defer rdb.Close()
	ctx := context.Background()
	rdb.Del(ctx, planning.JobsStream)
	defer rdb.Del(ctx, planning.JobsStream)

	ids := map[string]bool{}
	for range 25 {
		var job planning.Job
		e.call("POST", "/api/v1/optimization/jobs", "a", "", jobBody, &job)
		ids[job.ID.String()] = true
	}

	// Two relays race over the same rows; SKIP LOCKED must keep every event single.
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	var wg sync.WaitGroup
	for range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := &outbox.Relay{Pool: e.pool, Redis: rdb, Log: log, Batch: 4}
			for {
				n, err := r.PublishOnce(ctx)
				if err != nil {
					t.Error(err)
					return
				}
				if n == 0 {
					return
				}
			}
		}()
	}
	wg.Wait()

	msgs, err := rdb.XRange(ctx, planning.JobsStream, "-", "+").Result()
	if err != nil {
		t.Fatal(err)
	}
	if len(msgs) != len(ids) {
		t.Fatalf("published %d events for %d jobs", len(msgs), len(ids))
	}
	for _, m := range msgs {
		if !ids[m.Values["job_id"].(string)] {
			t.Errorf("unexpected event %+v", m.Values)
		}
	}
	var pending int
	_ = e.pool.QueryRow(ctx, "SELECT count(*) FROM core.outbox WHERE published_at IS NULL").Scan(&pending)
	if pending != 0 {
		t.Errorf("%d events left unpublished", pending)
	}
}
