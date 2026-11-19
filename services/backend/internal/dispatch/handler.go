package dispatch

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/dispatch/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

type PlanSummary struct {
	ID                uuid.UUID       `json:"id"`
	OptimizationJobID uuid.UUID       `json:"optimization_job_id"`
	PlanningDate      string          `json:"planning_date"`
	Version           int32           `json:"version"`
	Status            string          `json:"status"`
	Algorithm         string          `json:"algorithm"`
	TotalDistanceKm   float64         `json:"total_distance_km"`
	TotalCost         float64         `json:"total_cost"`
	Objective         json.RawMessage `json:"objective"`
	UnassignedCount   int32           `json:"unassigned_count"`
	RouteCount        int32           `json:"route_count"`
	CreatedAt         time.Time       `json:"created_at"`
	ApprovedAt        *time.Time      `json:"approved_at"`
	ActivatedAt       *time.Time      `json:"activated_at"`
}

type Plan struct {
	PlanSummary
	ApprovedBy         *uuid.UUID  `json:"approved_by"`
	UnassignedOrderIDs []uuid.UUID `json:"unassigned_order_ids"`
	Routes             []Route     `json:"routes"`
}

type Route struct {
	ID                       uuid.UUID  `json:"id"`
	RoutePlanID              uuid.UUID  `json:"route_plan_id"`
	VehicleID                uuid.UUID  `json:"vehicle_id"`
	RegistrationNumber       string     `json:"registration_number"`
	DriverID                 *uuid.UUID `json:"driver_id"`
	DriverName               *string    `json:"driver_name"`
	Status                   string     `json:"status"`
	TotalDistanceKm          float64    `json:"total_distance_km"`
	EstimatedDurationMinutes int32      `json:"estimated_duration_minutes"`
	Stops                    []Stop     `json:"stops,omitempty"`
}

type Stop struct {
	ID               uuid.UUID  `json:"id"`
	OrderID          uuid.UUID  `json:"order_id"`
	ExternalOrderID  string     `json:"external_order_id"`
	CustomerName     string     `json:"customer_name"`
	Lat              float64    `json:"lat"`
	Lon              float64    `json:"lon"`
	Sequence         int32      `json:"sequence"`
	PlannedArrival   time.Time  `json:"planned_arrival"`
	PlannedDeparture time.Time  `json:"planned_departure"`
	ActualArrival    *time.Time `json:"actual_arrival"`
	ActualDeparture  *time.Time `json:"actual_departure"`
	Status           string     `json:"status"`
	TimeWindowStart  time.Time  `json:"time_window_start"`
	TimeWindowEnd    time.Time  `json:"time_window_end"`
}

func toStop(s store.ListStopsForRouteRow) Stop {
	return Stop{s.ID, s.OrderID, s.ExternalOrderID, s.CustomerName, s.Lat, s.Lon, s.SequenceNumber,
		s.PlannedArrival, s.PlannedDeparture, s.ActualArrival, s.ActualDeparture, s.StopStatus,
		s.TimeWindowStart, s.TimeWindowEnd}
}

type Handler struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewHandler(pool *pgxpool.Pool, log *slog.Logger) *Handler { return &Handler{pool, log} }

// Routes mounts the route-plan API (spec §12.5): reads plus approve/reject/activate.
func (h *Handler) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	read := func(f httpx.HandlerFunc) http.Handler { return authn(httpx.Handle(h.log, f)) }
	mux.Handle("GET /api/v1/route-plans", read(h.listPlans))
	mux.Handle("GET /api/v1/route-plans/{id}", read(h.getPlan))
	mux.Handle("GET /api/v1/routes/{id}", read(h.getRoute))
	mux.Handle("GET /api/v1/routes/{id}/stops", read(h.getRouteStops))
	h.lifecycleRoutes(mux, authn)
}

func (h *Handler) listPlans(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := httpx.Page(r)
	if err != nil {
		return err
	}
	params := store.ListPlansParams{OrganizationID: auth.MustPrincipal(r).OrgID, Lim: limit, Off: offset}
	if v := r.URL.Query().Get("planning_date"); v != "" {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return httpx.BadRequest("INVALID_FILTER", "planning_date must be YYYY-MM-DD", nil)
		}
		params.PlanningDate = pgtype.Date{Time: d, Valid: true}
	}
	if v := strings.ToUpper(r.URL.Query().Get("status")); v != "" {
		params.Status = &v
	}
	rows, err := store.New(h.pool).ListPlans(r.Context(), params)
	if err != nil {
		return err
	}
	items := make([]PlanSummary, len(rows))
	for i, p := range rows {
		items[i] = PlanSummary{p.ID, p.OptimizationJobID, p.PlanningDate.Time.Format(time.DateOnly), p.Version,
			p.Status, p.Algorithm, p.TotalDistanceKm, p.TotalCost, p.Objective, p.UnassignedCount, p.RouteCount,
			p.CreatedAt, p.ApprovedAt, p.ActivatedAt}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}

// getPlan returns the plan with its routes and every stop, in route order.
func (h *Handler) getPlan(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "route plan")
	if err != nil {
		return err
	}
	plan, err := LoadPlan(r, h.pool, org, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, plan)
	return nil
}

// LoadPlan reads a full plan (routes and stops) for the organization.
func LoadPlan(r *http.Request, pool *pgxpool.Pool, org, id uuid.UUID) (Plan, error) {
	ctx := r.Context()
	q := store.New(pool)
	p, err := q.GetPlan(ctx, store.GetPlanParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Plan{}, httpx.NotFound("route plan")
	} else if err != nil {
		return Plan{}, err
	}
	routes, err := q.ListRoutesForPlan(ctx, store.ListRoutesForPlanParams{OrganizationID: org, RoutePlanID: id})
	if err != nil {
		return Plan{}, err
	}
	stops, err := q.ListStopsForPlan(ctx, store.ListStopsForPlanParams{OrganizationID: org, RoutePlanID: id})
	if err != nil {
		return Plan{}, err
	}
	byRoute := map[uuid.UUID][]Stop{}
	for _, s := range stops {
		byRoute[s.RouteID] = append(byRoute[s.RouteID], toStop(store.ListStopsForRouteRow(s)))
	}
	plan := Plan{
		PlanSummary: PlanSummary{p.ID, p.OptimizationJobID, p.PlanningDate.Time.Format(time.DateOnly), p.Version,
			p.Status, p.Algorithm, p.TotalDistanceKm, p.TotalCost, p.Objective, int32(len(p.UnassignedOrderIds)),
			int32(len(routes)), p.CreatedAt, p.ApprovedAt, p.ActivatedAt},
		ApprovedBy: p.ApprovedBy, UnassignedOrderIDs: p.UnassignedOrderIds, Routes: make([]Route, len(routes)),
	}
	for i, rt := range routes {
		plan.Routes[i] = Route{rt.ID, id, rt.VehicleID, rt.RegistrationNumber, rt.DriverID, rt.DriverName,
			rt.Status, rt.TotalDistanceKm, rt.EstimatedDurationMinutes, byRoute[rt.ID]}
	}
	return plan, nil
}

func (h *Handler) loadRoute(r *http.Request) (Route, error) {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "route")
	if err != nil {
		return Route{}, err
	}
	q := store.New(h.pool)
	rt, err := q.GetRoute(r.Context(), store.GetRouteParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Route{}, httpx.NotFound("route")
	} else if err != nil {
		return Route{}, err
	}
	rows, err := q.ListStopsForRoute(r.Context(), store.ListStopsForRouteParams{OrganizationID: org, RouteID: id})
	if err != nil {
		return Route{}, err
	}
	stops := make([]Stop, len(rows))
	for i, s := range rows {
		stops[i] = toStop(s)
	}
	return Route{rt.ID, rt.RoutePlanID, rt.VehicleID, rt.RegistrationNumber, rt.DriverID, rt.DriverName,
		rt.Status, rt.TotalDistanceKm, rt.EstimatedDurationMinutes, stops}, nil
}

func (h *Handler) getRoute(w http.ResponseWriter, r *http.Request) error {
	rt, err := h.loadRoute(r)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, rt)
	return nil
}

func (h *Handler) getRouteStops(w http.ResponseWriter, r *http.Request) error {
	rt, err := h.loadRoute(r)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"route_id": rt.ID, "items": rt.Stops})
	return nil
}
