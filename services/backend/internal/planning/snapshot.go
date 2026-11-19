package planning

import (
	"context"
	"fmt"
	"math"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/planning/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

// JobRequest is the body of POST /optimization/jobs (spec §12.7).
type JobRequest struct {
	PlanningDate     string      `json:"planning_date"`
	DepotIDs         []uuid.UUID `json:"depot_ids"`
	OrderIDs         []uuid.UUID `json:"order_ids"`
	VehicleIDs       []uuid.UUID `json:"vehicle_ids"`
	BatchID          *string     `json:"batch_id"`
	ObjectiveProfile string      `json:"objective_profile"`
	Solver           struct {
		Algorithm        string `json:"algorithm"`
		TimeLimitSeconds int    `json:"time_limit_seconds"`
		AllowUnassigned  *bool  `json:"allow_unassigned"`
		TimeWindows      string `json:"time_windows"`
		Seed             int    `json:"seed"`
	} `json:"solver"`
}

// buildSnapshot reads the day's problem (in the caller's REPEATABLE READ transaction) and converts
// it to contract units. Problems that make the request unusable are returned as a 422.
func buildSnapshot(ctx context.Context, q *store.Queries, org, jobID uuid.UUID, in JobRequest) (SolveRequest, error) {
	p := map[string]string{}
	day, err := time.Parse(time.DateOnly, in.PlanningDate)
	if err != nil {
		p["planning_date"] = "YYYY-MM-DD"
	}
	profile := defaultStr(in.ObjectiveProfile, "COST_BALANCED")
	weights, ok := profiles[profile]
	if !ok {
		p["objective_profile"] = "one of COST_BALANCED"
	}
	s := in.Solver
	cfg := SolverConfig{
		Algorithm:       defaultStr(s.Algorithm, "ORTOOLS_GLS"),
		TimeLimitS:      s.TimeLimitSeconds,
		AllowUnassigned: s.AllowUnassigned == nil || *s.AllowUnassigned,
		Seed:            s.Seed,
		TimeWindows:     defaultStr(s.TimeWindows, "SOFT"),
	}
	if cfg.TimeLimitS == 0 {
		cfg.TimeLimitS = 15
	}
	if !slices.Contains(algorithms, cfg.Algorithm) {
		p["solver.algorithm"] = "one of NEAREST_NEIGHBOUR, CLARKE_WRIGHT, ORTOOLS_GLS, HYBRID_GLS"
	}
	if cfg.TimeLimitS < 1 || cfg.TimeLimitS > 3600 {
		p["solver.time_limit_seconds"] = "between 1 and 3600"
	}
	if cfg.TimeWindows != "SOFT" && cfg.TimeWindows != "HARD" {
		p["solver.time_windows"] = "SOFT or HARD"
	}
	if len(p) > 0 {
		return SolveRequest{}, httpx.Unprocessable("VALIDATION_FAILED", "invalid job request", p)
	}

	tzName, err := q.OrganizationTimezone(ctx, org)
	if err != nil {
		return SolveRequest{}, err
	}
	tz, err := time.LoadLocation(tzName)
	if err != nil {
		return SolveRequest{}, err
	}
	midnight := time.Date(day.Year(), day.Month(), day.Day(), 0, 0, 0, 0, tz)
	date := pgtype.Date{Time: day, Valid: true}

	vrows, err := q.SnapshotVehicles(ctx, store.SnapshotVehiclesParams{
		OrganizationID: org, PlanningDate: date, VehicleIds: in.VehicleIDs, DepotIds: in.DepotIDs,
	})
	if err != nil {
		return SolveRequest{}, err
	}
	orows, err := q.SnapshotOrders(ctx, store.SnapshotOrdersParams{
		OrganizationID: org, PlanningDate: date, OrderIds: in.OrderIDs, BatchID: in.BatchID, DepotIds: in.DepotIDs,
	})
	if err != nil {
		return SolveRequest{}, err
	}
	if len(vrows) == 0 {
		return SolveRequest{}, httpx.Unprocessable("NO_VEHICLES",
			"no AVAILABLE vehicle is paired with an ACTIVE driver for this date (PUT /driver-assignments/{date})", nil)
	}
	if len(orows) == 0 {
		return SolveRequest{}, httpx.Unprocessable("NO_ORDERS", "no PENDING orders match this request", nil)
	}

	req := SolveRequest{
		ContractVersion: contractVersion, JobID: jobID.String(), Kind: "SOLVE",
		PlanningDate: in.PlanningDate, Timezone: tzName,
		Objective: Objective{Profile: profile, Weights: weights}, Solver: cfg,
	}
	depotIDs := map[uuid.UUID]bool{}
	var badVehicles []string
	for _, v := range vrows {
		start, end := clockSeconds(v.ShiftStart), clockSeconds(v.ShiftEnd)
		brk := int64(v.BreakDurationMinutes) * 60
		if end-brk <= start {
			badVehicles = append(badVehicles, v.RegistrationNumber)
			continue
		}
		depotIDs[v.DepotID] = true
		req.Vehicles = append(req.Vehicles, Vehicle{
			ID: v.ID.String(), DriverID: v.DriverID.String(), StartDepotID: v.DepotID.String(),
			CapWeightG: grams(v.CapacityWeightKg), CapVolumeCm3: cm3(v.CapacityVolumeM3),
			CostPerKm: v.CostPerKm, FixedCost: v.FixedCost,
			ShiftStartS: start, ShiftEndS: end, MaxOvertimeS: int64(v.MaxOvertimeMinutes) * 60, BreakS: brk,
		})
	}
	if len(badVehicles) > 0 {
		return SolveRequest{}, httpx.Unprocessable("INVALID_SHIFT",
			"the driver's break is not shorter than the shift", map[string]any{"vehicles": badVehicles})
	}

	var lateWindows []string
	for _, o := range orows {
		tws := int64(o.TimeWindowStart.Sub(midnight).Seconds())
		twe := int64(o.TimeWindowEnd.Sub(midnight).Seconds())
		if twe < 0 {
			lateWindows = append(lateWindows, o.ExternalOrderID)
			continue
		}
		var depot *string
		if o.DepotID != nil {
			depotIDs[*o.DepotID] = true
			s := o.DepotID.String()
			depot = &s
		}
		req.Orders = append(req.Orders, Order{
			ID: o.ID.String(), DepotID: depot, Lat: o.Lat, Lon: o.Lon,
			WeightG: grams(o.DemandWeightKg), VolumeCm3: cm3(o.DemandVolumeM3),
			TwStartS: max(tws, 0), TwEndS: twe,
			ServiceS: int64(o.ServiceDurationMinutes) * 60, Priority: o.Priority,
		})
	}
	if len(lateWindows) > 0 {
		return SolveRequest{}, httpx.Unprocessable("WINDOW_BEFORE_PLANNING_DATE",
			"these orders' delivery windows end before the planning date starts",
			map[string]any{"external_order_ids": lateWindows})
	}

	ids := make([]uuid.UUID, 0, len(depotIDs))
	for id := range depotIDs {
		ids = append(ids, id)
	}
	drows, err := q.SnapshotDepots(ctx, store.SnapshotDepotsParams{OrganizationID: org, Ids: ids})
	if err != nil {
		return SolveRequest{}, err
	}
	if len(drows) != len(ids) {
		return SolveRequest{}, fmt.Errorf("snapshot: %d of %d depots found", len(drows), len(ids))
	}
	for _, d := range drows {
		req.Depots = append(req.Depots, SnapshotDepot{ID: d.ID.String(), Lat: d.Lat, Lon: d.Lon})
	}
	return req, nil
}

func clockSeconds(t pgtype.Time) int64 { return t.Microseconds / 1_000_000 }
func grams(kg float64) int64           { return int64(math.Round(kg * 1000)) }
func cm3(m3 float64) int64             { return int64(math.Round(m3 * 1_000_000)) }

func defaultStr(v, d string) string {
	if v == "" {
		return d
	}
	return v
}
