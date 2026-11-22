// Package tracking owns what actually happens on the road (spec §6.1, §12.6): vehicle locations,
// route start, stop arrival and completion, and route progress.
//
// Route and stop lifecycle (only routes of the day's ACTIVE plan can move):
//
//	route: PLANNED --start--> IN_PROGRESS --(last stop finished)--> COMPLETED
//	stop:  PENDING --arrive--> ARRIVED --complete--> COMPLETED | FAILED
//	order: PLANNED --(route start)--> DISPATCHED --(stop finished)--> DELIVERED | FAILED
//
// Finished stops are frozen by the database (migration 00004), which is what lets re-optimization
// treat them as fixed. Repeating a transition that already happened returns 200 with the current
// progress, so clients on flaky networks can retry safely.
package tracking

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/tracking/store"
)

// ClockSkew is how far in the future a client-reported timestamp may be.
const ClockSkew = 2 * time.Minute

type Handler struct {
	pool *pgxpool.Pool
	log  *slog.Logger
	now  func() time.Time
}

func NewHandler(pool *pgxpool.Pool, log *slog.Logger) *Handler {
	return &Handler{pool, log, time.Now}
}

func (h *Handler) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	read := func(f httpx.HandlerFunc) http.Handler { return authn(httpx.Handle(h.log, f)) }
	write := func(f httpx.HandlerFunc) http.Handler {
		return authn(auth.RequireRole(httpx.Handle(h.log, f), auth.Writers...))
	}
	mux.Handle("POST /api/v1/vehicles/{id}/location", write(h.postLocation))
	mux.Handle("GET /api/v1/vehicles/{id}/location", read(h.getLocation))
	mux.Handle("POST /api/v1/routes/{id}/start", write(h.start))
	mux.Handle("POST /api/v1/routes/{id}/stops/{stopId}/arrive", write(h.arrive))
	mux.Handle("POST /api/v1/routes/{id}/stops/{stopId}/complete", write(h.complete))
	mux.Handle("GET /api/v1/routes/{id}/progress", read(h.getProgress))
}

// ---------------------------------------------------------------- locations

type Location struct {
	VehicleID  uuid.UUID  `json:"vehicle_id"`
	RouteID    *uuid.UUID `json:"route_id"`
	Lat        float64    `json:"lat"`
	Lon        float64    `json:"lon"`
	SpeedKmh   *float64   `json:"speed_kmh"`
	Heading    *float64   `json:"heading"`
	RecordedAt time.Time  `json:"recorded_at"`
}

// postLocation records a GPS fix. It is linked to the route the vehicle is driving, if any.
func (h *Handler) postLocation(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Lat        *float64   `json:"lat"`
		Lon        *float64   `json:"lon"`
		SpeedKmh   *float64   `json:"speed_kmh"`
		Heading    *float64   `json:"heading"`
		RecordedAt *time.Time `json:"recorded_at"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	var problems []map[string]string
	bad := func(field, msg string) {
		problems = append(problems, map[string]string{"field": field, "message": msg})
	}
	if in.Lat == nil || *in.Lat < -90 || *in.Lat > 90 || math.IsNaN(*in.Lat) {
		bad("lat", "required, in [-90, 90]")
	}
	if in.Lon == nil || *in.Lon < -180 || *in.Lon > 180 || math.IsNaN(*in.Lon) {
		bad("lon", "required, in [-180, 180]")
	}
	if in.SpeedKmh != nil && (*in.SpeedKmh < 0 || *in.SpeedKmh >= 1000) {
		bad("speed_kmh", "must be in [0, 1000)")
	}
	if in.Heading != nil && (*in.Heading < 0 || *in.Heading >= 360) {
		bad("heading", "must be in [0, 360)")
	}
	at, err := h.resolveAt(in.RecordedAt, "recorded_at")
	if err != nil {
		return err
	}
	if problems != nil {
		return httpx.Unprocessable("VALIDATION_FAILED", "invalid location", problems)
	}

	org := auth.MustPrincipal(r).OrgID
	vehicle, err := httpx.PathUUID(r, "id", "vehicle")
	if err != nil {
		return err
	}
	q := store.New(h.pool)
	if ok, err := q.VehicleExists(r.Context(), store.VehicleExistsParams{OrganizationID: org, ID: vehicle}); err != nil {
		return err
	} else if !ok {
		return httpx.NotFound("vehicle")
	}
	var route *uuid.UUID
	if id, err := q.CurrentRouteForVehicle(r.Context(), store.CurrentRouteForVehicleParams{OrganizationID: org, VehicleID: vehicle}); err == nil {
		route = &id
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if err := q.InsertGPSEvent(r.Context(), store.InsertGPSEventParams{
		OrganizationID: org, VehicleID: vehicle, RouteID: route, Lat: *in.Lat, Lon: *in.Lon,
		SpeedKmh: in.SpeedKmh, Heading: in.Heading, RecordedAt: at,
	}); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, Location{vehicle, route, *in.Lat, *in.Lon, in.SpeedKmh, in.Heading, at})
	return nil
}

func (h *Handler) getLocation(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	vehicle, err := httpx.PathUUID(r, "id", "vehicle")
	if err != nil {
		return err
	}
	q := store.New(h.pool)
	if ok, err := q.VehicleExists(r.Context(), store.VehicleExistsParams{OrganizationID: org, ID: vehicle}); err != nil {
		return err
	} else if !ok {
		return httpx.NotFound("vehicle")
	}
	l, err := q.LatestVehicleLocation(r.Context(), store.LatestVehicleLocationParams{OrganizationID: org, VehicleID: vehicle})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("vehicle location")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, Location(l))
	return nil
}

// ---------------------------------------------------------------- route progress

type ProgressStop struct {
	ID              uuid.UUID  `json:"id"`
	OrderID         uuid.UUID  `json:"order_id"`
	ExternalOrderID string     `json:"external_order_id"`
	CustomerName    string     `json:"customer_name"`
	Sequence        int32      `json:"sequence"`
	Status          string     `json:"status"`
	PlannedArrival  time.Time  `json:"planned_arrival"`
	ActualArrival   *time.Time `json:"actual_arrival"`
	ActualDeparture *time.Time `json:"actual_departure"`
	FailureReason   *string    `json:"failure_reason"`
}

type Progress struct {
	RouteID            uuid.UUID      `json:"route_id"`
	RoutePlanID        uuid.UUID      `json:"route_plan_id"`
	PlanStatus         string         `json:"plan_status"`
	PlanningDate       string         `json:"planning_date"`
	VehicleID          uuid.UUID      `json:"vehicle_id"`
	RegistrationNumber string         `json:"registration_number"`
	Status             string         `json:"status"`
	StartedAt          *time.Time     `json:"started_at"`
	CompletedAt        *time.Time     `json:"completed_at"`
	StopsTotal         int            `json:"stops_total"`
	StopsCompleted     int            `json:"stops_completed"`
	StopsFailed        int            `json:"stops_failed"`
	StopsRemaining     int            `json:"stops_remaining"` // PENDING or ARRIVED
	CurrentStop        *ProgressStop  `json:"current_stop"`    // the ARRIVED stop, if any
	NextStop           *ProgressStop  `json:"next_stop"`       // the first PENDING stop by sequence
	DelayMinutes       *int           `json:"delay_minutes"`   // latest actual arrival vs its plan; negative = early
	LastLocation       *Location      `json:"last_location"`
	Stops              []ProgressStop `json:"stops"`
}

func (h *Handler) loadProgress(ctx context.Context, org, id uuid.UUID) (Progress, error) {
	q := store.New(h.pool)
	rt, err := q.GetRoute(ctx, store.GetRouteParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Progress{}, httpx.NotFound("route")
	} else if err != nil {
		return Progress{}, err
	}
	rows, err := q.ListStops(ctx, id)
	if err != nil {
		return Progress{}, err
	}
	p := Progress{
		RouteID: rt.ID, RoutePlanID: rt.RoutePlanID, PlanStatus: rt.PlanStatus,
		PlanningDate: rt.PlanningDate.Time.Format(time.DateOnly), VehicleID: rt.VehicleID,
		RegistrationNumber: rt.RegistrationNumber, Status: rt.Status, StartedAt: rt.StartedAt,
		CompletedAt: rt.CompletedAt, StopsTotal: len(rows), Stops: make([]ProgressStop, len(rows)),
	}
	var latest *ProgressStop
	for i, s := range rows {
		p.Stops[i] = ProgressStop{s.ID, s.OrderID, s.ExternalOrderID, s.CustomerName, s.SequenceNumber, s.StopStatus,
			s.PlannedArrival, s.ActualArrival, s.ActualDeparture, s.FailureReason}
		stop := &p.Stops[i]
		switch s.StopStatus {
		case "COMPLETED":
			p.StopsCompleted++
		case "FAILED":
			p.StopsFailed++
		case "ARRIVED":
			p.StopsRemaining++
			p.CurrentStop = stop
		case "PENDING":
			p.StopsRemaining++
			if p.NextStop == nil {
				p.NextStop = stop
			}
		}
		if s.ActualArrival != nil && (latest == nil || s.ActualArrival.After(*latest.ActualArrival)) {
			latest = stop
		}
	}
	if latest != nil {
		d := int(math.Round(latest.ActualArrival.Sub(latest.PlannedArrival).Minutes()))
		p.DelayMinutes = &d
	}
	if l, err := q.LatestRouteLocation(ctx, &id); err == nil {
		loc := Location(l)
		p.LastLocation = &loc
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return Progress{}, err
	}
	return p, nil
}

func (h *Handler) getProgress(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "route")
	if err != nil {
		return err
	}
	p, err := h.loadProgress(r.Context(), auth.MustPrincipal(r).OrgID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, p)
	return nil
}

// ---------------------------------------------------------------- transitions

// onRoute locks an ACTIVE plan's route and runs fn in one transaction, then answers with progress.
func (h *Handler) onRoute(w http.ResponseWriter, r *http.Request, fn func(q *store.Queries, rt store.LockRouteRow) error) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "route")
	if err != nil {
		return err
	}
	err = database.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rt, err := q.LockRoute(r.Context(), store.LockRouteParams{OrganizationID: org, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("route")
		} else if err != nil {
			return err
		}
		if rt.PlanStatus != "ACTIVE" {
			return httpx.Conflict("PLAN_NOT_ACTIVE", "the route's plan is "+rt.PlanStatus+"; only routes of the ACTIVE plan can be tracked")
		}
		return fn(q, rt)
	})
	if err != nil {
		return err
	}
	return h.getProgress(w, r)
}

func (h *Handler) start(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		At *time.Time `json:"at"`
	}
	if err := h.decodeOptional(w, r, &in); err != nil {
		return err
	}
	at, err := h.resolveAt(in.At, "at")
	if err != nil {
		return err
	}
	return h.onRoute(w, r, func(q *store.Queries, rt store.LockRouteRow) error {
		switch rt.Status {
		case "IN_PROGRESS":
			return nil // already started
		case "PLANNED":
		default:
			return httpx.Conflict("INVALID_ROUTE_STATE", "the route is "+rt.Status+"; only a PLANNED route can start")
		}
		err := q.StartRoute(r.Context(), store.StartRouteParams{ID: rt.ID, At: &at})
		if database.IsUniqueViolation(err, "routes_one_in_progress_idx") {
			return httpx.Conflict("VEHICLE_BUSY", "this vehicle is already driving another route")
		} else if err != nil {
			return err
		}
		_, err = q.DispatchRouteOrders(r.Context(), rt.ID)
		return err
	})
}

func (h *Handler) arrive(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		At *time.Time `json:"at"`
	}
	if err := h.decodeOptional(w, r, &in); err != nil {
		return err
	}
	at, err := h.resolveAt(in.At, "at")
	if err != nil {
		return err
	}
	return h.onRoute(w, r, func(q *store.Queries, rt store.LockRouteRow) error {
		stop, err := h.lockStop(r, q, rt)
		if err != nil {
			return err
		}
		switch stop.StopStatus {
		case "ARRIVED":
			return nil // already here
		case "PENDING":
		default:
			return httpx.Conflict("INVALID_STOP_STATE", "the stop is "+stop.StopStatus+"; only a PENDING stop can be arrived at")
		}
		if err := requireInProgress(rt); err != nil {
			return err
		}
		if rt.StartedAt != nil && at.Before(*rt.StartedAt) {
			return httpx.Unprocessable("INVALID_TIME", "arrival is before the route started", nil)
		}
		err = q.ArriveStop(r.Context(), store.ArriveStopParams{ID: stop.ID, At: &at})
		if database.IsUniqueViolation(err, "route_stops_one_arrived_idx") {
			return httpx.Conflict("VEHICLE_AT_ANOTHER_STOP", "finish the current stop before arriving at the next")
		}
		return err
	})
}

func (h *Handler) complete(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Status        string     `json:"status"` // COMPLETED (default) or FAILED
		FailureReason *string    `json:"failure_reason"`
		At            *time.Time `json:"at"`
	}
	if err := h.decodeOptional(w, r, &in); err != nil {
		return err
	}
	in.Status = strings.ToUpper(in.Status)
	if in.Status == "" {
		in.Status = "COMPLETED"
	}
	var reason *string
	if in.FailureReason != nil {
		if s := strings.TrimSpace(*in.FailureReason); s != "" {
			reason = &s
		}
	}
	switch {
	case in.Status != "COMPLETED" && in.Status != "FAILED":
		return httpx.Unprocessable("VALIDATION_FAILED", "status must be COMPLETED or FAILED", nil)
	case in.Status == "FAILED" && reason == nil:
		return httpx.Unprocessable("VALIDATION_FAILED", "a FAILED stop needs a failure_reason", nil)
	case in.Status == "COMPLETED" && reason != nil:
		return httpx.Unprocessable("VALIDATION_FAILED", "failure_reason is only for FAILED stops", nil)
	case reason != nil && len([]rune(*reason)) > 200:
		return httpx.Unprocessable("VALIDATION_FAILED", "failure_reason is at most 200 characters", nil)
	}
	at, err := h.resolveAt(in.At, "at")
	if err != nil {
		return err
	}
	return h.onRoute(w, r, func(q *store.Queries, rt store.LockRouteRow) error {
		stop, err := h.lockStop(r, q, rt)
		if err != nil {
			return err
		}
		switch stop.StopStatus {
		case in.Status:
			return nil // already finished this way
		case "ARRIVED":
		default:
			return httpx.Conflict("INVALID_STOP_STATE", "the stop is "+stop.StopStatus+"; only an ARRIVED stop can be completed")
		}
		if err := requireInProgress(rt); err != nil {
			return err
		}
		if at.Before(*stop.ActualArrival) {
			return httpx.Unprocessable("INVALID_TIME", "departure is before the arrival", nil)
		}
		ctx := r.Context()
		if err := q.FinishStop(ctx, store.FinishStopParams{ID: stop.ID, StopStatus: in.Status, At: &at, FailureReason: reason}); err != nil {
			return err
		}
		outcome := map[string]string{"COMPLETED": "DELIVERED", "FAILED": "FAILED"}[in.Status]
		if err := q.SetOrderOutcome(ctx, store.SetOrderOutcomeParams{ID: stop.OrderID, Status: outcome}); err != nil {
			return err
		}
		open, err := q.CountOpenStops(ctx, rt.ID)
		if err != nil || open > 0 {
			return err
		}
		return q.CompleteRoute(ctx, store.CompleteRouteParams{ID: rt.ID, At: &at})
	})
}

// lockStop locks one of the route's stops.
func (h *Handler) lockStop(r *http.Request, q *store.Queries, rt store.LockRouteRow) (store.LockStopRow, error) {
	id, err := httpx.PathUUID(r, "stopId", "stop")
	if err != nil {
		return store.LockStopRow{}, err
	}
	stop, err := q.LockStop(r.Context(), store.LockStopParams{RouteID: rt.ID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return store.LockStopRow{}, httpx.NotFound("stop")
	} else if err != nil {
		return store.LockStopRow{}, err
	}
	return stop, nil
}

func requireInProgress(rt store.LockRouteRow) error {
	if rt.Status != "IN_PROGRESS" {
		return httpx.Conflict("INVALID_ROUTE_STATE", "the route is "+rt.Status+"; stops change only while it is IN_PROGRESS")
	}
	return nil
}

// ---------------------------------------------------------------- helpers

func (h *Handler) decodeOptional(w http.ResponseWriter, r *http.Request, dst any) error {
	if r.ContentLength == 0 {
		return nil
	}
	return httpx.DecodeJSON(w, r, dst)
}

// resolveAt defaults a client timestamp to now and rejects ones from the future.
func (h *Handler) resolveAt(at *time.Time, field string) (time.Time, error) {
	now := h.now()
	if at == nil {
		return now, nil
	}
	if at.After(now.Add(ClockSkew)) {
		return time.Time{}, httpx.Unprocessable("INVALID_TIME", field+" is in the future", nil)
	}
	return *at, nil
}
