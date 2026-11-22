package tracking_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/tracking"
)

type fixture struct {
	t        *testing.T
	ctx      context.Context
	pool     *pgxpool.Pool
	url      string
	org      uuid.UUID
	vehicles []uuid.UUID
	orders   []uuid.UUID // O-1, O-2 on V1's route; O-3 on V2's route
	route    uuid.UUID   // V1's route on the ACTIVE plan
	stops    []uuid.UUID // its two stops, in sequence
	other    uuid.UUID   // V2's route on the ACTIVE plan
}

func setup(t *testing.T) *fixture {
	pool, _ := testdb.New(t)
	f := &fixture{t: t, ctx: context.Background(), pool: pool}
	f.org = testdb.CreateOrg(t, pool, "org-a")
	orgB := testdb.CreateOrg(t, pool, "org-b")
	depot := f.id(`INSERT INTO core.depots (organization_id, name, location) VALUES ($1, 'BBSR', 'SRID=4326;POINT(85.82 20.29)') RETURNING id`, f.org)
	for i := range 2 {
		f.vehicles = append(f.vehicles, f.id(`INSERT INTO operations.vehicles (organization_id, depot_id, registration_number,
			vehicle_type, capacity_weight_kg, capacity_volume_m3, cost_per_km) VALUES ($1, $2, $3, 'van', 500, 4, 12) RETURNING id`,
			f.org, depot, fmt.Sprintf("V%d", i+1)))
	}
	for i := range 3 {
		f.orders = append(f.orders, f.id(`INSERT INTO operations.orders (organization_id, external_order_id, planning_date,
			customer_name, delivery_location, time_window_start, time_window_end, status)
			VALUES ($1, $2, '2026-11-02', 'C', 'SRID=4326;POINT(85.83 20.30)', '2026-11-02T09:00+05:30', '2026-11-02T13:00+05:30', 'PLANNED')
			RETURNING id`, f.org, fmt.Sprintf("O-%d", i+1)))
	}
	plan := f.plan("2026-11-02", "ACTIVE")
	f.route = f.routeFor(plan, f.vehicles[0])
	f.stops = []uuid.UUID{
		f.stop(f.route, f.orders[0], 1, "2026-11-02T09:10+05:30"),
		f.stop(f.route, f.orders[1], 2, "2026-11-02T09:40+05:30"),
	}
	f.other = f.routeFor(plan, f.vehicles[1])
	f.stop(f.other, f.orders[2], 1, "2026-11-02T11:00+05:30")

	v := authtest.Verifier{
		"a":      {UserID: uuid.New(), OrgID: f.org, Role: auth.RoleDispatcher},
		"viewer": {UserID: uuid.New(), OrgID: f.org, Role: auth.RoleViewer},
		"b":      {UserID: uuid.New(), OrgID: orgB, Role: auth.RoleAdmin},
	}
	mux := http.NewServeMux()
	tracking.NewHandler(pool, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	f.url = srv.URL
	return f
}

func (f *fixture) id(sql string, args ...any) uuid.UUID {
	f.t.Helper()
	var v uuid.UUID
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&v); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (f *fixture) plan(day, status string) uuid.UUID {
	job := f.id(`INSERT INTO optimization.jobs (organization_id, status, planning_date, input_snapshot)
		VALUES ($1, 'COMPLETED', $2, '{}') RETURNING id`, f.org, day)
	return f.id(`INSERT INTO routing.route_plans (organization_id, optimization_job_id, planning_date, version, status,
		total_distance_km, total_cost) VALUES ($1, $2, $3,
		(SELECT coalesce(max(version), 0) + 1 FROM routing.route_plans WHERE planning_date = $3), $4, 10, 100) RETURNING id`,
		f.org, job, day, status)
}

func (f *fixture) routeFor(plan, vehicle uuid.UUID) uuid.UUID {
	return f.id(`INSERT INTO routing.routes (organization_id, route_plan_id, vehicle_id, total_distance_km, estimated_duration_minutes)
		VALUES ($1, $2, $3, 5, 60) RETURNING id`, f.org, plan, vehicle)
}

func (f *fixture) stop(route, order uuid.UUID, seq int, planned string) uuid.UUID {
	return f.id(`INSERT INTO routing.route_stops (organization_id, route_id, order_id, sequence_number, planned_arrival, planned_departure)
		VALUES ($1, $2, $3, $4, $5::timestamptz, $5::timestamptz + interval '5 minutes') RETURNING id`, f.org, route, order, seq, planned)
}

func (f *fixture) str(sql string, args ...any) string {
	var s string
	if err := f.pool.QueryRow(f.ctx, sql, args...).Scan(&s); err != nil {
		f.t.Fatalf("%s: %v", sql, err)
	}
	return s
}

func (f *fixture) orderStatus(id uuid.UUID) string {
	return f.str("SELECT status FROM operations.orders WHERE id = $1", id)
}

func (f *fixture) do(method, path, token string, body, out any) (int, string) {
	f.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, f.url+path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		f.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	var e struct{ Error struct{ Code string } }
	_ = json.Unmarshal(raw, &e)
	return resp.StatusCode, e.Error.Code
}

func routePath(route uuid.UUID, action string) string {
	return "/api/v1/routes/" + route.String() + "/" + action
}

func stopPath(route, stop uuid.UUID, action string) string {
	return routePath(route, "stops/"+stop.String()+"/"+action)
}

func ts(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestRouteLifecycle(t *testing.T) {
	f := setup(t)
	var p tracking.Progress

	if code, ec := f.do("POST", stopPath(f.route, f.stops[0], "arrive"), "a", nil, nil); code != 409 || ec != "INVALID_ROUTE_STATE" {
		t.Errorf("arrive before start = %d %s", code, ec)
	}
	if code, _ := f.do("POST", routePath(f.route, "start"), "viewer", nil, nil); code != 403 {
		t.Errorf("viewer start = %d, want 403", code)
	}
	start := map[string]any{"at": ts("2026-11-02T08:30:00+05:30")}
	if code, _ := f.do("POST", routePath(f.route, "start"), "a", start, &p); code != 200 || p.Status != "IN_PROGRESS" ||
		p.StartedAt == nil || !p.StartedAt.Equal(ts("2026-11-02T08:30:00+05:30")) {
		t.Fatalf("start = %d %+v", code, p)
	}
	if code, _ := f.do("POST", routePath(f.route, "start"), "a", nil, &p); code != 200 || !p.StartedAt.Equal(ts("2026-11-02T08:30:00+05:30")) {
		t.Errorf("repeated start = %d, started_at %v (must not move)", code, p.StartedAt)
	}
	for i, want := range []string{"DISPATCHED", "DISPATCHED", "PLANNED"} {
		if s := f.orderStatus(f.orders[i]); s != want {
			t.Errorf("order O-%d after start = %s, want %s", i+1, s, want)
		}
	}

	// Arrive at stop 1 twelve minutes late, try to jump to stop 2, then deliver.
	arrive1 := map[string]any{"at": ts("2026-11-02T09:22:00+05:30")}
	if code, _ := f.do("POST", stopPath(f.route, f.stops[0], "arrive"), "a", arrive1, &p); code != 200 ||
		p.CurrentStop == nil || p.CurrentStop.ID != f.stops[0] || p.DelayMinutes == nil || *p.DelayMinutes != 12 {
		t.Fatalf("arrive = %d %+v", code, p)
	}
	if code, ec := f.do("POST", stopPath(f.route, f.stops[1], "arrive"), "a", nil, nil); code != 409 || ec != "VEHICLE_AT_ANOTHER_STOP" {
		t.Errorf("arrive at a second stop = %d %s", code, ec)
	}
	if code, ec := f.do("POST", stopPath(f.route, f.stops[1], "complete"), "a", nil, nil); code != 409 || ec != "INVALID_STOP_STATE" {
		t.Errorf("complete a PENDING stop = %d %s", code, ec)
	}
	early := map[string]any{"at": ts("2026-11-02T09:00:00+05:30")}
	if code, ec := f.do("POST", stopPath(f.route, f.stops[0], "complete"), "a", early, nil); code != 422 || ec != "INVALID_TIME" {
		t.Errorf("departure before arrival = %d %s", code, ec)
	}
	done1 := map[string]any{"at": ts("2026-11-02T09:27:00+05:30")}
	if code, _ := f.do("POST", stopPath(f.route, f.stops[0], "complete"), "a", done1, &p); code != 200 ||
		p.StopsCompleted != 1 || p.StopsRemaining != 1 || p.CurrentStop != nil || p.NextStop == nil || p.NextStop.ID != f.stops[1] {
		t.Fatalf("complete = %d %+v", code, p)
	}
	if s := f.orderStatus(f.orders[0]); s != "DELIVERED" {
		t.Errorf("delivered order is %s", s)
	}
	if code, _ := f.do("POST", stopPath(f.route, f.stops[0], "complete"), "a", nil, nil); code != 200 {
		t.Errorf("repeated complete = %d, want 200", code)
	}
	if code, ec := f.do("POST", stopPath(f.route, f.stops[0], "complete"), "a", map[string]string{"status": "FAILED", "failure_reason": "x"}, nil); code != 409 || ec != "INVALID_STOP_STATE" {
		t.Errorf("re-finish a COMPLETED stop as FAILED = %d %s", code, ec)
	}

	// Stop 2 fails; that finishes the route.
	f.do("POST", stopPath(f.route, f.stops[1], "arrive"), "a", map[string]any{"at": ts("2026-11-02T09:35:00+05:30")}, nil)
	if code, ec := f.do("POST", stopPath(f.route, f.stops[1], "complete"), "a", map[string]string{"status": "FAILED"}, nil); code != 422 || ec != "VALIDATION_FAILED" {
		t.Errorf("FAILED without reason = %d %s", code, ec)
	}
	failed := map[string]any{"status": "failed", "failure_reason": " customer absent ", "at": ts("2026-11-02T09:40:00+05:30")}
	if code, _ := f.do("POST", stopPath(f.route, f.stops[1], "complete"), "a", failed, &p); code != 200 ||
		p.Status != "COMPLETED" || p.CompletedAt == nil || p.StopsFailed != 1 || p.StopsRemaining != 0 || p.NextStop != nil ||
		p.Stops[1].FailureReason == nil || *p.Stops[1].FailureReason != "customer absent" || *p.DelayMinutes != -5 {
		t.Fatalf("fail last stop = %d %+v", code, p)
	}
	if s := f.orderStatus(f.orders[1]); s != "FAILED" {
		t.Errorf("failed order is %s", s)
	}
	// Retrying the last call after the route closed still succeeds.
	if code, _ := f.do("POST", stopPath(f.route, f.stops[1], "complete"), "a", failed, nil); code != 200 {
		t.Errorf("retry on completed route = %d, want 200", code)
	}
	if code, ec := f.do("POST", routePath(f.route, "start"), "a", nil, nil); code != 409 || ec != "INVALID_ROUTE_STATE" {
		t.Errorf("restart a COMPLETED route = %d %s", code, ec)
	}

	// The database freezes finished stops, whatever the caller.
	if _, err := f.pool.Exec(f.ctx, "UPDATE routing.route_stops SET sequence_number = 9 WHERE id = $1", f.stops[0]); err == nil {
		t.Error("a COMPLETED stop was modified")
	}
}

func TestDelayComparesServiceStarts(t *testing.T) {
	f := setup(t)
	f.do("POST", routePath(f.route, "start"), "a", map[string]any{"at": ts("2026-11-02T08:00:00+05:30")}, nil)
	// Planned service start 09:10, window opens 09:00: arriving at 08:30 means waiting until 09:00.
	var p tracking.Progress
	if code, _ := f.do("POST", stopPath(f.route, f.stops[0], "arrive"), "a",
		map[string]any{"at": ts("2026-11-02T08:30:00+05:30")}, &p); code != 200 || p.DelayMinutes == nil || *p.DelayMinutes != -10 {
		t.Fatalf("early arrival = %d, delay %v, want -10", code, p.DelayMinutes)
	}
}

func TestTrackingGuards(t *testing.T) {
	f := setup(t)

	if code, _ := f.do("POST", routePath(f.route, "start"), "b", nil, nil); code != 404 {
		t.Errorf("other org = %d, want 404", code)
	}
	if code, _ := f.do("GET", routePath(f.route, "progress"), "b", nil, nil); code != 404 {
		t.Errorf("other org progress = %d, want 404", code)
	}
	if code, _ := f.do("POST", stopPath(f.route, uuid.New(), "arrive"), "a", nil, nil); code != 404 {
		t.Errorf("unknown stop = %d, want 404", code)
	}
	if code, _ := f.do("POST", stopPath(f.other, f.stops[0], "arrive"), "a", nil, nil); code != 404 {
		t.Errorf("stop of another route = %d, want 404", code)
	}
	future := map[string]any{"at": time.Now().Add(time.Hour)}
	if code, ec := f.do("POST", routePath(f.route, "start"), "a", future, nil); code != 422 || ec != "INVALID_TIME" {
		t.Errorf("future start = %d %s", code, ec)
	}
	if code, ec := f.do("POST", routePath(f.route, "start"), "a", map[string]any{"bogus": 1}, nil); code != 400 || ec != "INVALID_JSON" {
		t.Errorf("unknown field = %d %s", code, ec)
	}

	// Routes of a plan that is not ACTIVE cannot be driven.
	draft := f.routeFor(f.plan("2026-11-03", "DRAFT"), f.vehicles[0])
	if code, ec := f.do("POST", routePath(draft, "start"), "a", nil, nil); code != 409 || ec != "PLAN_NOT_ACTIVE" {
		t.Errorf("start a DRAFT route = %d %s", code, ec)
	}

	// One vehicle drives one route at a time.
	tomorrow := f.routeFor(f.plan("2026-11-04", "ACTIVE"), f.vehicles[0])
	if code, _ := f.do("POST", routePath(f.route, "start"), "a", nil, nil); code != 200 {
		t.Fatalf("start = %d", code)
	}
	if code, ec := f.do("POST", routePath(tomorrow, "start"), "a", nil, nil); code != 409 || ec != "VEHICLE_BUSY" {
		t.Errorf("second route for a busy vehicle = %d %s", code, ec)
	}
	if code, _ := f.do("POST", routePath(f.other, "start"), "a", nil, nil); code != 200 {
		t.Errorf("another vehicle = %d, want 200", code)
	}

	if code, ec := f.do("POST", stopPath(f.route, f.stops[0], "arrive"), "a", map[string]any{"at": ts("2020-01-01T00:00:00Z")}, nil); code != 422 || ec != "INVALID_TIME" {
		t.Errorf("arrival before route start = %d %s", code, ec)
	}
}

func TestVehicleLocations(t *testing.T) {
	f := setup(t)
	vehicle := "/api/v1/vehicles/" + f.vehicles[0].String() + "/location"

	if code, _ := f.do("GET", vehicle, "a", nil, nil); code != 404 {
		t.Errorf("no fixes yet = %d, want 404", code)
	}
	var l tracking.Location
	at := time.Now().Add(-time.Minute).UTC().Truncate(time.Second)
	depot := map[string]any{"lat": 20.29, "lon": 85.82, "recorded_at": at.Add(-10 * time.Minute)}
	if code, _ := f.do("POST", vehicle, "a", depot, &l); code != 201 || l.RouteID != nil {
		t.Fatalf("fix before the route starts = %d %+v", code, l)
	}

	f.do("POST", routePath(f.route, "start"), "a", nil, nil)
	fix := map[string]any{"lat": 20.30, "lon": 85.83, "speed_kmh": 32.5, "heading": 90, "recorded_at": at}
	if code, _ := f.do("POST", vehicle, "a", fix, &l); code != 201 || l.RouteID == nil || *l.RouteID != f.route {
		t.Fatalf("fix on route = %d %+v", code, l)
	}
	// An older fix arriving late does not become the latest position.
	f.do("POST", vehicle, "a", map[string]any{"lat": 1, "lon": 1, "recorded_at": at.Add(-time.Hour)}, nil)

	if code, _ := f.do("GET", vehicle, "viewer", nil, &l); code != 200 || l.Lat != 20.30 || l.Lon != 85.83 ||
		l.SpeedKmh == nil || *l.SpeedKmh != 32.5 || !l.RecordedAt.Equal(at) {
		t.Errorf("latest = %d %+v", code, l)
	}
	var p tracking.Progress
	if code, _ := f.do("GET", routePath(f.route, "progress"), "viewer", nil, &p); code != 200 || p.LastLocation == nil || p.LastLocation.Lat != 20.30 {
		t.Errorf("progress location = %d %+v", code, p.LastLocation)
	}

	var bad struct {
		Error struct {
			Code    string
			Details []map[string]string
		}
	}
	if code, _ := f.do("POST", vehicle, "a", map[string]any{"lat": 95, "heading": 360}, &bad); code != 422 || len(bad.Error.Details) != 3 {
		t.Errorf("invalid fix = %d %+v", code, bad)
	}
	if code, _ := f.do("POST", vehicle, "viewer", map[string]any{"lat": 1, "lon": 1}, nil); code != 403 {
		t.Errorf("viewer post = %d, want 403", code)
	}
	if code, _ := f.do("POST", "/api/v1/vehicles/"+uuid.NewString()+"/location", "a", map[string]any{"lat": 1, "lon": 1}, nil); code != 404 {
		t.Errorf("unknown vehicle = %d, want 404", code)
	}
	if code, _ := f.do("GET", vehicle, "b", nil, nil); code != 404 {
		t.Errorf("other org read = %d, want 404", code)
	}
}
