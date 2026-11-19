// Package planning owns the optimization-job lifecycle (spec §6): it snapshots the day's problem,
// enqueues it through the transactional outbox, and exposes job status and results.
package planning

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

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/planning/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

// JobsStream is the Redis stream the optimizer worker consumes (docs/ARCHITECTURE.md §2).
const JobsStream = "opti:jobs"

type Job struct {
	ID              uuid.UUID  `json:"id"`
	Kind            string     `json:"kind"`
	Status          string     `json:"status"`
	PlanningDate    string     `json:"planning_date"`
	Algorithm       string     `json:"algorithm"`
	OrderCount      int32      `json:"order_count"`
	VehicleCount    int32      `json:"vehicle_count"`
	ObjectiveValue  *float64   `json:"objective_value"`
	TotalDistanceKm *float64   `json:"total_distance_km"`
	ExecutionTimeMs *int32     `json:"execution_time_ms"`
	ErrorMessage    *string    `json:"error_message"`
	CancelRequested bool       `json:"cancel_requested"`
	Attempts        int32      `json:"attempts"`
	CreatedAt       time.Time  `json:"created_at"`
	StartedAt       *time.Time `json:"started_at"`
	CompletedAt     *time.Time `json:"completed_at"`
}

func toJob(j store.GetJobRow) Job {
	date := ""
	if j.PlanningDate.Valid {
		date = j.PlanningDate.Time.Format(time.DateOnly)
	}
	return Job{j.ID, j.Kind, j.Status, date, j.Algorithm, j.OrderCount, j.VehicleCount, j.ObjectiveValue,
		j.TotalDistanceKm, j.ExecutionTimeMs, j.ErrorMessage, j.CancelRequested, j.Attempts,
		j.CreatedAt, j.StartedAt, j.CompletedAt}
}

type Handler struct {
	pool *pgxpool.Pool
	log  *slog.Logger
}

func NewHandler(pool *pgxpool.Pool, log *slog.Logger) *Handler { return &Handler{pool, log} }

func (h *Handler) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	read := func(f httpx.HandlerFunc) http.Handler { return authn(httpx.Handle(h.log, f)) }
	write := func(f httpx.HandlerFunc) http.Handler {
		return authn(auth.RequireRole(httpx.Handle(h.log, f), auth.Writers...))
	}
	mux.Handle("POST /api/v1/optimization/jobs", write(h.create))
	mux.Handle("GET /api/v1/optimization/jobs", read(h.list))
	mux.Handle("GET /api/v1/optimization/jobs/{id}", read(h.get))
	mux.Handle("GET /api/v1/optimization/jobs/{id}/result", read(h.result))
	mux.Handle("POST /api/v1/optimization/jobs/{id}/cancel", write(h.cancel))
}

// create snapshots the problem and enqueues it. With an Idempotency-Key header, retries of the
// same request return the original job (200) instead of creating another (202).
func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	key := strings.TrimSpace(r.Header.Get("Idempotency-Key"))
	if len(key) > 128 {
		return httpx.BadRequest("INVALID_IDEMPOTENCY_KEY", "Idempotency-Key is at most 128 characters", nil)
	}
	var in JobRequest
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if key != "" {
		if id, err := store.New(h.pool).FindJobByIdempotencyKey(r.Context(), store.FindJobByIdempotencyKeyParams{OrganizationID: org, IdempotencyKey: &key}); err == nil {
			return h.writeJob(w, r, org, id, http.StatusOK)
		} else if !errors.Is(err, pgx.ErrNoRows) {
			return err
		}
	}

	jobID := uuid.New()
	tx, err := h.pool.BeginTx(r.Context(), pgx.TxOptions{IsoLevel: pgx.RepeatableRead})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(r.Context()) }()
	q := store.New(tx)

	snapshot, err := buildSnapshot(r.Context(), q, org, jobID, in)
	if err != nil {
		return err
	}
	snapJSON, _ := json.Marshal(snapshot)
	cfgJSON, _ := json.Marshal(snapshot.Solver)
	payload, _ := json.Marshal(map[string]string{"job_id": jobID.String()})
	day, _ := time.Parse(time.DateOnly, snapshot.PlanningDate)

	var keyParam *string
	if key != "" {
		keyParam = &key
	}
	err = q.InsertJob(r.Context(), store.InsertJobParams{
		ID: jobID, OrganizationID: org, PlanningDate: pgtype.Date{Time: day, Valid: true},
		IdempotencyKey: keyParam, InputSnapshot: snapJSON, SolverConfig: cfgJSON,
	})
	if database.IsUniqueViolation(err, "jobs_organization_id_idempotency_key_key") {
		// A concurrent request with the same key won the race: answer with its job.
		_ = tx.Rollback(r.Context())
		id, err := store.New(h.pool).FindJobByIdempotencyKey(r.Context(), store.FindJobByIdempotencyKeyParams{OrganizationID: org, IdempotencyKey: &key})
		if err != nil {
			return err
		}
		return h.writeJob(w, r, org, id, http.StatusOK)
	} else if err != nil {
		return err
	}
	if err := q.InsertOutbox(r.Context(), store.InsertOutboxParams{Topic: JobsStream, Payload: payload}); err != nil {
		return err
	}
	if err := tx.Commit(r.Context()); err != nil {
		return err
	}
	w.Header().Set("Location", "/api/v1/optimization/jobs/"+jobID.String())
	return h.writeJob(w, r, org, jobID, http.StatusAccepted)
}

func (h *Handler) writeJob(w http.ResponseWriter, r *http.Request, org, id uuid.UUID, status int) error {
	j, err := store.New(h.pool).GetJob(r.Context(), store.GetJobParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("optimization job")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, status, toJob(j))
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "optimization job")
	if err != nil {
		return err
	}
	return h.writeJob(w, r, auth.MustPrincipal(r).OrgID, id, http.StatusOK)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := httpx.Page(r)
	if err != nil {
		return err
	}
	params := store.ListJobsParams{OrganizationID: auth.MustPrincipal(r).OrgID, Lim: limit, Off: offset}
	if v := r.URL.Query().Get("planning_date"); v != "" {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return httpx.BadRequest("INVALID_FILTER", "planning_date must be YYYY-MM-DD", nil)
		}
		params.PlanningDate = pgtype.Date{Time: d, Valid: true}
	}
	rows, err := store.New(h.pool).ListJobs(r.Context(), params)
	if err != nil {
		return err
	}
	items := make([]Job, len(rows))
	for i, j := range rows {
		items[i] = toJob(store.GetJobRow(j))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}

// result returns the optimizer's SolveResult as stored (404 until the job has one).
func (h *Handler) result(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "optimization job")
	if err != nil {
		return err
	}
	row, err := store.New(h.pool).GetJobResult(r.Context(), store.GetJobResultParams{OrganizationID: auth.MustPrincipal(r).OrgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("optimization job")
	} else if err != nil {
		return err
	}
	if row.Result == nil {
		return &httpx.Error{Status: http.StatusNotFound, Code: "RESULT_NOT_READY",
			Message: "the job has no result yet (status " + row.Status + ")"}
	}
	w.Header().Set("Content-Type", "application/json")
	_, err = w.Write(row.Result)
	return err
}

func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "optimization job")
	if err != nil {
		return err
	}
	q := store.New(h.pool)
	if _, err := q.RequestCancel(r.Context(), store.RequestCancelParams{OrganizationID: org, ID: id}); errors.Is(err, pgx.ErrNoRows) {
		status, err := q.JobStatus(r.Context(), store.JobStatusParams{OrganizationID: org, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("optimization job")
		} else if err != nil {
			return err
		}
		return httpx.Conflict("JOB_FINISHED", "the job already finished with status "+status)
	} else if err != nil {
		return err
	}
	return h.writeJob(w, r, org, id, http.StatusAccepted)
}
