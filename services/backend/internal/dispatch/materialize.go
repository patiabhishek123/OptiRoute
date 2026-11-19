// Package dispatch owns route plans: it turns completed optimization jobs into DRAFT plans, serves
// them, and (next) approves and activates them (spec §6). Draft plans never change order state;
// orders move only when a plan is activated, because several drafts may compete for one day.
package dispatch

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/dispatch/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
)

// solveResult is the part of the optimizer's SolveResult (contract v1) that a plan needs.
type solveResult struct {
	Status    string          `json:"status"`
	Objective json.RawMessage `json:"objective"`
	Kpis      *struct {
		DistanceM int64 `json:"distance_m"`
	} `json:"kpis"`
	Routes []struct {
		VehicleID string  `json:"vehicle_id"`
		DriverID  *string `json:"driver_id"`
		DistanceM int64   `json:"distance_m"`
		DurationS int64   `json:"duration_s"`
		Stops     []struct {
			OrderID    string `json:"order_id"`
			Seq        int32  `json:"seq"`
			ArrivalS   int64  `json:"arrival_s"`
			DepartureS int64  `json:"departure_s"`
		} `json:"stops"`
	} `json:"routes"`
	Unassigned []struct {
		OrderID string `json:"order_id"`
	} `json:"unassigned"`
	Meta struct {
		Algorithm string `json:"algorithm"`
	} `json:"meta"`
}

// ErrMalformedResult marks results that can never become a plan (retrying will not help).
var ErrMalformedResult = errors.New("malformed optimization result")

// Materialize creates the DRAFT plan for a COMPLETED job. It is idempotent: a job that already has
// a plan returns that plan with created=false. Jobs in any other status are skipped (uuid.Nil).
func Materialize(ctx context.Context, pool *pgxpool.Pool, jobID uuid.UUID) (planID uuid.UUID, created bool, err error) {
	err = database.WithTx(ctx, pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		job, err := q.GetJobForPlan(ctx, jobID)
		if errors.Is(err, pgx.ErrNoRows) {
			return fmt.Errorf("%w: job %s does not exist", ErrMalformedResult, jobID)
		} else if err != nil {
			return err
		}
		if job.Status != "COMPLETED" {
			return nil
		}
		var res solveResult
		if err := json.Unmarshal(job.Result, &res); err != nil || res.Kpis == nil || res.Status != "FEASIBLE" {
			return fmt.Errorf("%w: job %s (%v)", ErrMalformedResult, jobID, err)
		}
		tz, err := time.LoadLocation(job.Timezone)
		if err != nil || !job.PlanningDate.Valid {
			return fmt.Errorf("%w: job %s has no usable timezone/planning date", ErrMalformedResult, jobID)
		}
		d := job.PlanningDate.Time
		midnight := time.Date(d.Year(), d.Month(), d.Day(), 0, 0, 0, 0, tz)
		at := func(s int64) time.Time { return midnight.Add(time.Duration(s) * time.Second) }

		org := job.OrganizationID
		if err := q.LockPlanningDay(ctx, org.String()+"/"+d.Format(time.DateOnly)); err != nil {
			return err
		}
		version, err := q.NextPlanVersion(ctx, store.NextPlanVersionParams{OrganizationID: org, PlanningDate: job.PlanningDate})
		if err != nil {
			return err
		}
		unassigned := make([]uuid.UUID, 0, len(res.Unassigned))
		for _, u := range res.Unassigned {
			id, err := uuid.Parse(u.OrderID)
			if err != nil {
				return fmt.Errorf("%w: unassigned order id %q", ErrMalformedResult, u.OrderID)
			}
			unassigned = append(unassigned, id)
		}
		var total struct {
			Total float64 `json:"total"`
		}
		_ = json.Unmarshal(res.Objective, &total)

		planID, err = q.InsertPlan(ctx, store.InsertPlanParams{
			OrganizationID: org, OptimizationJobID: jobID, PlanningDate: job.PlanningDate, Version: version,
			TotalDistanceKm: float64(res.Kpis.DistanceM) / 1000, TotalCost: total.Total,
			Algorithm: res.Meta.Algorithm, Objective: res.Objective, UnassignedOrderIds: unassigned,
		})
		if errors.Is(err, pgx.ErrNoRows) { // ON CONFLICT: this job already has a plan
			return nil
		} else if err != nil {
			return err
		}
		created = true

		for _, r := range res.Routes {
			vehicle, err := uuid.Parse(r.VehicleID)
			if err != nil {
				return fmt.Errorf("%w: vehicle id %q", ErrMalformedResult, r.VehicleID)
			}
			var driver *uuid.UUID
			if r.DriverID != nil {
				id, err := uuid.Parse(*r.DriverID)
				if err != nil {
					return fmt.Errorf("%w: driver id %q", ErrMalformedResult, *r.DriverID)
				}
				driver = &id
			}
			routeID, err := q.InsertRoute(ctx, store.InsertRouteParams{
				OrganizationID: org, RoutePlanID: planID, VehicleID: vehicle, DriverID: driver,
				TotalDistanceKm: float64(r.DistanceM) / 1000, EstimatedDurationMinutes: int32(math.Ceil(float64(r.DurationS) / 60)),
			})
			if err != nil {
				return err
			}
			stops := make([]store.InsertStopsParams, len(r.Stops))
			for i, s := range r.Stops {
				order, err := uuid.Parse(s.OrderID)
				if err != nil {
					return fmt.Errorf("%w: order id %q", ErrMalformedResult, s.OrderID)
				}
				stops[i] = store.InsertStopsParams{
					OrganizationID: org, RouteID: routeID, OrderID: order, SequenceNumber: s.Seq,
					PlannedArrival: at(s.ArrivalS), PlannedDeparture: at(s.DepartureS),
				}
			}
			if _, err := q.InsertStops(ctx, stops); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return uuid.Nil, false, err
	}
	if !created && planID == uuid.Nil {
		// Already materialized (or not COMPLETED): report the existing plan if there is one.
		err = pool.QueryRow(ctx, "SELECT id FROM routing.route_plans WHERE optimization_job_id = $1", jobID).Scan(&planID)
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, false, nil
		}
	}
	return planID, created, err
}
