package fleet_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/fleet"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
)

type env struct {
	t              *testing.T
	url            string
	depotA, depotB uuid.UUID
}

func depot(t *testing.T, pool *pgxpool.Pool, org uuid.UUID) uuid.UUID {
	var id uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO core.depots (organization_id, name, location)
		VALUES ($1, 'hub', 'SRID=4326;POINT(85.82 20.29)') RETURNING id`, org).Scan(&id); err != nil {
		t.Fatal(err)
	}
	return id
}

func setup(t *testing.T) env {
	pool, _ := testdb.New(t)
	orgA, orgB := testdb.CreateOrg(t, pool, "org-a"), testdb.CreateOrg(t, pool, "org-b")
	v := authtest.Verifier{
		"a":        {UserID: uuid.New(), OrgID: orgA, Role: auth.RoleDispatcher},
		"viewer-a": {UserID: uuid.New(), OrgID: orgA, Role: auth.RoleViewer},
		"b":        {UserID: uuid.New(), OrgID: orgB, Role: auth.RoleAdmin},
	}
	mux := http.NewServeMux()
	fleet.NewHandler(pool, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return env{t, srv.URL, depot(t, pool, orgA), depot(t, pool, orgB)}
}

func (e env) call(method, path, token string, body, out any) int {
	e.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, e.url+path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
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

func (e env) vehicle(reg string, depot uuid.UUID) (fleet.Vehicle, int) {
	var v fleet.Vehicle
	code := e.call("POST", "/api/v1/vehicles", "a", map[string]any{
		"depot_id": depot, "registration_number": reg, "vehicle_type": "van",
		"capacity_weight_kg": 500, "capacity_volume_m3": 4, "cost_per_km": 12.5, "fixed_cost": 800,
	}, &v)
	return v, code
}

func TestVehicles(t *testing.T) {
	e := setup(t)
	v, code := e.vehicle("OD-02-AB-1234", e.depotA)
	if code != 201 || v.Status != "AVAILABLE" || v.CostPerKm != 12.5 || v.CapacityWeightKg != 500 {
		t.Fatalf("create = %d %+v", code, v)
	}
	if _, code := e.vehicle("OD-02-AB-1234", e.depotA); code != 409 {
		t.Errorf("duplicate registration = %d, want 409", code)
	}
	if _, code := e.vehicle("OD-02-XX-9999", e.depotB); code != 422 {
		t.Errorf("other org's depot = %d, want 422", code)
	}

	var patched fleet.Vehicle
	if code := e.call("PATCH", "/api/v1/vehicles/"+v.ID.String(), "a", map[string]any{"capacity_weight_kg": 750, "status": "maintenance"}, &patched); code != 200 {
		t.Fatalf("patch = %d", code)
	}
	if patched.CapacityWeightKg != 750 || patched.Status != "MAINTENANCE" || patched.CostPerKm != 12.5 {
		t.Errorf("patched %+v", patched)
	}
	var down fleet.Vehicle
	e.call("POST", "/api/v1/vehicles/"+v.ID.String()+"/unavailable", "a", nil, &down)
	if down.Status != "UNAVAILABLE" {
		t.Errorf("unavailable: %+v", down)
	}

	e.vehicle("OD-02-AB-0001", e.depotA)
	var list struct{ Items []fleet.Vehicle }
	e.call("GET", "/api/v1/vehicles?status=AVAILABLE&depot_id="+e.depotA.String(), "viewer-a", nil, &list)
	if len(list.Items) != 1 || list.Items[0].RegistrationNumber != "OD-02-AB-0001" {
		t.Errorf("filtered list: %+v", list.Items)
	}

	if code := e.call("GET", "/api/v1/vehicles/"+v.ID.String(), "b", nil, nil); code != 404 {
		t.Errorf("other org get = %d, want 404", code)
	}
	if _, code := e.vehicleAs("viewer-a"); code != 403 {
		t.Errorf("viewer create = %d, want 403", code)
	}
	if code := e.call("POST", "/api/v1/vehicles", "a", map[string]any{"vehicle_type": "van", "cost_per_km": -1}, nil); code != 422 {
		t.Errorf("invalid vehicle = %d, want 422", code)
	}
}

func (e env) vehicleAs(token string) (fleet.Vehicle, int) {
	var v fleet.Vehicle
	code := e.call("POST", "/api/v1/vehicles", token, map[string]any{
		"depot_id": e.depotA, "registration_number": "R", "vehicle_type": "van",
		"capacity_weight_kg": 1, "capacity_volume_m3": 1, "cost_per_km": 1,
	}, &v)
	return v, code
}

func TestDrivers(t *testing.T) {
	e := setup(t)
	var d fleet.Driver
	if code := e.call("POST", "/api/v1/drivers", "a", map[string]any{
		"name": "Asha Pradhan", "phone": "+91-9000000000", "shift_start": "08:00", "shift_end": "18:30",
		"break_duration_minutes": 30, "max_overtime_minutes": 60,
	}, &d); code != 201 {
		t.Fatalf("create = %d", code)
	}
	if d.ShiftStart != "08:00" || d.ShiftEnd != "18:30" || d.Status != "ACTIVE" || d.BreakDurationMinutes != 30 {
		t.Fatalf("driver %+v", d)
	}
	if code := e.call("POST", "/api/v1/drivers", "a", map[string]any{"name": "X", "shift_start": "18:00", "shift_end": "08:00"}, nil); code != 422 {
		t.Errorf("reversed shift = %d, want 422", code)
	}
	if code := e.call("POST", "/api/v1/drivers", "a", map[string]any{"name": "X", "shift_start": "8am", "shift_end": "17:00"}, nil); code != 422 {
		t.Errorf("bad clock = %d, want 422", code)
	}
	// Only shift_end in the PATCH, earlier than the stored shift_start: the DB CHECK catches it.
	if code := e.call("PATCH", "/api/v1/drivers/"+d.ID.String(), "a", map[string]any{"shift_end": "07:00"}, nil); code != 422 {
		t.Errorf("patch shift_end before stored start = %d, want 422", code)
	}
	var p fleet.Driver
	e.call("PATCH", "/api/v1/drivers/"+d.ID.String(), "a", map[string]any{"shift_end": "17:00", "status": "inactive"}, &p)
	if p.ShiftEnd != "17:00" || p.ShiftStart != "08:00" || p.Status != "INACTIVE" {
		t.Errorf("patched %+v", p)
	}
}

func TestDailyPairing(t *testing.T) {
	e := setup(t)
	v1, _ := e.vehicle("V1", e.depotA)
	v2, _ := e.vehicle("V2", e.depotA)
	var d1, d2 fleet.Driver
	for _, d := range []*fleet.Driver{&d1, &d2} {
		e.call("POST", "/api/v1/drivers", "a", map[string]any{"name": "D", "shift_start": "08:00", "shift_end": "17:00"}, d)
	}
	path := "/api/v1/driver-assignments/2026-11-02"

	var got struct{ Assignments []fleet.Assignment }
	if code := e.call("PUT", path, "a", map[string]any{"assignments": []map[string]any{
		{"vehicle_id": v1.ID, "driver_id": d1.ID}, {"vehicle_id": v2.ID, "driver_id": d2.ID},
	}}, &got); code != 200 || len(got.Assignments) != 2 || got.Assignments[0].RegistrationNumber != "V1" {
		t.Fatalf("put = %d %+v", code, got)
	}

	// Driver d1 twice: rejected, and the previous pairing must survive (one transaction).
	if code := e.call("PUT", path, "a", map[string]any{"assignments": []map[string]any{
		{"vehicle_id": v1.ID, "driver_id": d1.ID}, {"vehicle_id": v2.ID, "driver_id": d1.ID},
	}}, nil); code != 422 {
		t.Fatalf("double booking = %d, want 422", code)
	}
	e.call("GET", path, "viewer-a", nil, &got)
	if len(got.Assignments) != 2 || got.Assignments[1].DriverID != d2.ID {
		t.Fatalf("pairing changed after a rejected PUT: %+v", got.Assignments)
	}

	if code := e.call("PUT", path, "b", map[string]any{"assignments": []map[string]any{
		{"vehicle_id": v1.ID, "driver_id": d1.ID},
	}}, nil); code != 422 {
		t.Errorf("other org pairing our vehicle = %d, want 422", code)
	}
	if code := e.call("PUT", "/api/v1/driver-assignments/02-11-2026", "a", map[string]any{"assignments": []any{}}, nil); code != 400 {
		t.Errorf("bad date = %d, want 400", code)
	}
	e.call("PUT", path, "a", map[string]any{"assignments": []any{}}, &got)
	if len(got.Assignments) != 0 {
		t.Errorf("clearing the day left %+v", got.Assignments)
	}
}
