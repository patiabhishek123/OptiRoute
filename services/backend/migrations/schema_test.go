package migrations_test

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
	"github.com/patiabhishek123/OptiRoute/services/backend/migrations"
)

func TestMigrationsRoundTrip(t *testing.T) {
	_, url := testdb.New(t) // already migrated up
	db, err := sql.Open("pgx", url)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if _, err := p.DownTo(ctx, 0); err != nil {
		t.Fatalf("down: %v", err)
	}
	if _, err := p.Up(ctx); err != nil {
		t.Fatalf("up again: %v", err)
	}
}

// fixture creates two organizations, each with a depot, vehicle, driver and order.
type fixture struct {
	orgA, orgB, depotA, depotB, vehA, vehB, drvA, ordA, ordB string
}

func seed(t *testing.T, pool *pgxpool.Pool) fixture {
	t.Helper()
	ctx := context.Background()
	var f fixture
	one := func(dst *string, q string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, q, args...).Scan(dst); err != nil {
			t.Fatalf("seed %q: %v", q, err)
		}
	}
	for _, o := range []struct {
		id         *string
		slug       string
		depot, veh *string
	}{{&f.orgA, "org-a", &f.depotA, &f.vehA}, {&f.orgB, "org-b", &f.depotB, &f.vehB}} {
		one(o.id, `INSERT INTO core.organizations (name, slug) VALUES ($1, $1) RETURNING id`, o.slug)
		one(o.depot, `INSERT INTO core.depots (organization_id, name, location)
		              VALUES ($1, 'hub', 'SRID=4326;POINT(85.82 20.29)') RETURNING id`, *o.id)
		one(o.veh, `INSERT INTO operations.vehicles (organization_id, depot_id, registration_number,
		              vehicle_type, capacity_weight_kg, capacity_volume_m3, cost_per_km)
		            VALUES ($1, $2, 'OD-02-0001', 'van', 500, 4, 12) RETURNING id`, *o.id, *o.depot)
	}
	one(&f.drvA, `INSERT INTO operations.drivers (organization_id, name, shift_start, shift_end)
	              VALUES ($1, 'Asha', '08:00', '18:00') RETURNING id`, f.orgA)
	order := `INSERT INTO operations.orders (organization_id, external_order_id, planning_date,
	            customer_name, delivery_location, time_window_start, time_window_end)
	          VALUES ($1, 'ORD-1', '2026-11-02', 'C', 'SRID=4326;POINT(85.83 20.30)',
	                  '2026-11-02T09:00+05:30', '2026-11-02T13:00+05:30') RETURNING id`
	one(&f.ordA, order, f.orgA)
	one(&f.ordB, order, f.orgB)
	return f
}

func pgCode(err error) string {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		return pgErr.Code
	}
	return ""
}

const (
	foreignKeyViolation = "23503"
	uniqueViolation     = "23505"
)

func TestSchemaInvariants(t *testing.T) {
	pool, _ := testdb.New(t)
	f := seed(t, pool)
	ctx := context.Background()

	// A plan in org A with one route, used by the routing checks below.
	var jobA, planA, routeA string
	mustScan := func(dst *string, q string, args ...any) {
		t.Helper()
		if err := pool.QueryRow(ctx, q, args...).Scan(dst); err != nil {
			t.Fatalf("%q: %v", q, err)
		}
	}
	mustScan(&jobA, `INSERT INTO optimization.jobs (organization_id, input_snapshot) VALUES ($1, '{}') RETURNING id`, f.orgA)
	planSQL := `INSERT INTO routing.route_plans (organization_id, optimization_job_id, planning_date,
	              version, status, total_distance_km, total_cost)
	            VALUES ($1, $2, '2026-11-02', $3, $4, 10, 100) RETURNING id`
	mustScan(&planA, planSQL, f.orgA, jobA, 1, "ACTIVE")
	mustScan(&routeA, `INSERT INTO routing.routes (organization_id, route_plan_id, vehicle_id, driver_id,
	                     total_distance_km, estimated_duration_minutes)
	                   VALUES ($1, $2, $3, $4, 10, 60) RETURNING id`, f.orgA, planA, f.vehA, f.drvA)

	cases := []struct {
		name string
		sql  string
		args []any
		code string
	}{
		{"vehicle cannot use another tenant's depot",
			`INSERT INTO operations.vehicles (organization_id, depot_id, registration_number, vehicle_type,
			   capacity_weight_kg, capacity_volume_m3, cost_per_km) VALUES ($1, $2, 'X', 'van', 1, 1, 1)`,
			[]any{f.orgA, f.depotB}, foreignKeyViolation},
		{"route cannot use another tenant's vehicle",
			`INSERT INTO routing.routes (organization_id, route_plan_id, vehicle_id, total_distance_km,
			   estimated_duration_minutes) VALUES ($1, $2, $3, 1, 1)`,
			[]any{f.orgA, planA, f.vehB}, foreignKeyViolation},
		{"stop cannot reference another tenant's order",
			`INSERT INTO routing.route_stops (organization_id, route_id, order_id, sequence_number,
			   planned_arrival, planned_departure) VALUES ($1, $2, $3, 1, now(), now())`,
			[]any{f.orgA, routeA, f.ordB}, foreignKeyViolation},
		{"job cannot belong to a missing organization",
			`INSERT INTO optimization.jobs (organization_id, input_snapshot) VALUES (gen_random_uuid(), '{}')`,
			nil, foreignKeyViolation},
		{"second ACTIVE plan for the same day",
			`INSERT INTO routing.route_plans (organization_id, optimization_job_id, planning_date, version,
			   status, total_distance_km, total_cost)
			 SELECT $1, id, '2026-11-02', 2, 'ACTIVE', 1, 1 FROM optimization.jobs WHERE id = $2`,
			[]any{f.orgA, jobA}, uniqueViolation},
		{"driver double-booked on one day",
			`WITH v2 AS (INSERT INTO operations.vehicles (organization_id, depot_id, registration_number,
			   vehicle_type, capacity_weight_kg, capacity_volume_m3, cost_per_km)
			   VALUES ($1, $2, 'OD-02-0002', 'van', 1, 1, 1) RETURNING id)
			 INSERT INTO operations.driver_assignments (organization_id, planning_date, vehicle_id, driver_id)
			 SELECT $1::uuid, DATE '2026-11-02', id, $3::uuid FROM v2
			 UNION ALL SELECT $1::uuid, DATE '2026-11-02', $4::uuid, $3::uuid`,
			[]any{f.orgA, f.depotA, f.drvA, f.vehA}, uniqueViolation},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := pool.Exec(ctx, c.sql, c.args...)
			if got := pgCode(err); got != c.code {
				t.Fatalf("got %v (code %q), want SQLSTATE %s", err, got, c.code)
			}
		})
	}

	t.Run("an order has at most one active assignment", func(t *testing.T) {
		assign := `INSERT INTO routing.order_assignments (order_id, organization_id, route_id, route_plan_id)
		           VALUES ($1, $2, $3, $4)`
		if _, err := pool.Exec(ctx, assign, f.ordA, f.orgA, routeA, planA); err != nil {
			t.Fatal(err)
		}
		_, err := pool.Exec(ctx, assign, f.ordA, f.orgA, routeA, planA)
		if pgCode(err) != uniqueViolation {
			t.Fatalf("got %v, want unique violation", err)
		}
	})
}
