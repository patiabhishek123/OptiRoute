package dispatch

import (
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/dispatch/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

// Plan lifecycle (spec §11.1, §12.5):
//
//	DRAFT --approve--> APPROVED --activate--> ACTIVE --(another plan activated)--> SUPERSEDED
//	DRAFT, APPROVED --reject--> REJECTED
//
// Published plans are immutable: transitions only change status, never routes or stops.

func (h *Handler) lifecycleRoutes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	write := func(f httpx.HandlerFunc) http.Handler {
		return authn(auth.RequireRole(httpx.Handle(h.log, f), auth.Writers...))
	}
	mux.Handle("POST /api/v1/route-plans/{id}/approve", write(h.approve))
	mux.Handle("POST /api/v1/route-plans/{id}/reject", write(h.reject))
	mux.Handle("POST /api/v1/route-plans/{id}/activate", write(h.activate))
}

// transition locks the plan and runs fn if its status is one of from.
func (h *Handler) transition(r *http.Request, from []string, fn func(q *store.Queries, plan store.LockPlanRow) error) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "route plan")
	if err != nil {
		return err
	}
	return database.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		plan, err := q.LockPlan(r.Context(), store.LockPlanParams{OrganizationID: org, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("route plan")
		} else if err != nil {
			return err
		}
		if !slices.Contains(from, plan.Status) {
			return httpx.Conflict("INVALID_PLAN_STATE", "the plan is "+plan.Status+"; this action needs "+joinOr(from))
		}
		return fn(q, plan)
	})
}

func (h *Handler) approve(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustPrincipal(r)
	err := h.transition(r, []string{"DRAFT"}, func(q *store.Queries, plan store.LockPlanRow) error {
		return q.ApprovePlan(r.Context(), store.ApprovePlanParams{ID: plan.ID, ApprovedBy: &p.UserID})
	})
	if err != nil {
		return err
	}
	return h.getPlan(w, r)
}

func (h *Handler) reject(w http.ResponseWriter, r *http.Request) error {
	err := h.transition(r, []string{"DRAFT", "APPROVED"}, func(q *store.Queries, plan store.LockPlanRow) error {
		return q.RejectPlan(r.Context(), plan.ID)
	})
	if err != nil {
		return err
	}
	return h.getPlan(w, r)
}

type activation struct {
	Plan              Plan        `json:"plan"`
	SupersededPlanID  *uuid.UUID  `json:"superseded_plan_id"`
	UnplannedOrderIDs []uuid.UUID `json:"unplanned_order_ids"` // from the replaced plan, back to PENDING
}

// activate makes an APPROVED plan the day's ACTIVE plan in one transaction (spec §11.1 step 10).
func (h *Handler) activate(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		ReplaceActive bool `json:"replace_active"`
	}
	if r.ContentLength != 0 {
		if err := httpx.DecodeJSON(w, r, &in); err != nil {
			return err
		}
	}
	org := auth.MustPrincipal(r).OrgID
	out := activation{UnplannedOrderIDs: []uuid.UUID{}}

	err := h.transition(r, []string{"APPROVED"}, func(q *store.Queries, plan store.LockPlanRow) error {
		ctx := r.Context()
		// Same lock as plan creation: one activation per organization and day at a time.
		if err := q.LockPlanningDay(ctx, org.String()+"/"+plan.PlanningDate.Time.Format(time.DateOnly)); err != nil {
			return err
		}
		active, err := q.ActivePlanForDay(ctx, store.ActivePlanForDayParams{OrganizationID: org, PlanningDate: plan.PlanningDate})
		hasActive := err == nil
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
		if hasActive {
			if !in.ReplaceActive {
				return httpx.Conflict("ACTIVE_PLAN_EXISTS",
					`this day already has an ACTIVE plan; send {"replace_active": true} to replace it`)
			}
			started, err := q.PlanHasStarted(ctx, active)
			if err != nil {
				return err
			}
			if started {
				return httpx.Conflict("ACTIVE_PLAN_IN_PROGRESS",
					"the active plan's routes have started; changing them requires re-optimization")
			}
		}

		var replaced *uuid.UUID
		if hasActive {
			replaced = &active
		}
		stale, err := q.StaleOrders(ctx, store.StaleOrdersParams{RoutePlanID: plan.ID, ReplacedPlanID: replaced})
		if err != nil {
			return err
		}
		if len(stale) > 0 {
			details := make([]map[string]string, len(stale))
			for i, s := range stale {
				details[i] = map[string]string{"order_id": s.ID.String(), "external_order_id": s.ExternalOrderID, "status": s.Status}
			}
			return &httpx.Error{Status: http.StatusConflict, Code: "STALE_PLAN",
				Message: "orders in this plan changed since it was optimized; re-optimize", Details: details}
		}

		var previous []uuid.UUID
		if hasActive {
			if err := q.SupersedePlan(ctx, active); err != nil {
				return err
			}
			if previous, err = q.DeleteAssignmentsForPlan(ctx, active); err != nil {
				return err
			}
			out.SupersededPlanID = replaced
		}
		if _, err := q.InsertAssignmentsForPlan(ctx, plan.ID); err != nil {
			return err
		}
		if _, err := q.MarkPlanOrdersPlanned(ctx, plan.ID); err != nil {
			return err
		}
		if len(previous) > 0 {
			reverted, err := q.RevertUnassignedOrders(ctx, previous)
			if err != nil {
				return err
			}
			out.UnplannedOrderIDs = append(out.UnplannedOrderIDs, reverted...)
		}
		if err := q.ActivatePlan(ctx, plan.ID); err != nil {
			return err
		}
		payload, _ := json.Marshal(map[string]string{
			"plan_id": plan.ID.String(), "organization_id": org.String(), "status": "ACTIVE",
			"planning_date": plan.PlanningDate.Time.Format(time.DateOnly),
		})
		return q.InsertPlanEvent(ctx, payload)
	})
	if err != nil {
		return err
	}
	id, _ := httpx.PathUUID(r, "id", "route plan")
	if out.Plan, err = LoadPlan(r, h.pool, org, id); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, out)
	return nil
}

func joinOr(states []string) string {
	switch len(states) {
	case 1:
		return states[0]
	default:
		return states[0] + " or " + joinOr(states[1:])
	}
}
