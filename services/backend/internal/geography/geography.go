// Package geography owns depots and their coordinates (spec §6).
package geography

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/geography/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

type Depot struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Lat       float64   `json:"lat"`
	Lon       float64   `json:"lon"`
	IsActive  bool      `json:"is_active"`
	CreatedAt time.Time `json:"created_at"`
}

type Service struct{ pool *pgxpool.Pool }

func NewService(pool *pgxpool.Pool) *Service { return &Service{pool: pool} }

// Get returns one of the organization's depots (used by other modules, e.g. planning).
func (s *Service) Get(ctx context.Context, org, id uuid.UUID) (Depot, error) {
	d, err := store.New(s.pool).GetDepot(ctx, store.GetDepotParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return Depot{}, httpx.NotFound("depot")
	}
	return Depot{d.ID, d.Name, d.Lat, d.Lon, d.IsActive, d.CreatedAt}, err
}

type Handler struct {
	svc *Service
	log *slog.Logger
}

func NewHandler(svc *Service, log *slog.Logger) *Handler { return &Handler{svc, log} }

// Routes mounts the depot API (spec §12.3). Reading needs any role; writing needs a writer role.
func (h *Handler) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	read := func(f httpx.HandlerFunc) http.Handler { return authn(httpx.Handle(h.log, f)) }
	write := func(f httpx.HandlerFunc) http.Handler {
		return authn(auth.RequireRole(httpx.Handle(h.log, f), auth.Writers...))
	}
	mux.Handle("POST /api/v1/depots", write(h.create))
	mux.Handle("GET /api/v1/depots", read(h.list))
	mux.Handle("GET /api/v1/depots/{id}", read(h.get))
	mux.Handle("PATCH /api/v1/depots/{id}", write(h.update))
}

type depotInput struct {
	Name     *string  `json:"name"`
	Lat      *float64 `json:"lat"`
	Lon      *float64 `json:"lon"`
	IsActive *bool    `json:"is_active"`
}

// validate checks the fields that are present; create additionally requires all of them.
func (in *depotInput) validate(create bool) error {
	problems := map[string]string{}
	if in.Name != nil {
		*in.Name = strings.TrimSpace(*in.Name)
	}
	if (create && in.Name == nil) || (in.Name != nil && (*in.Name == "" || len(*in.Name) > 200)) {
		problems["name"] = "1-200 characters"
	}
	if (in.Lat == nil) != (in.Lon == nil) || (create && in.Lat == nil) {
		problems["location"] = "lat and lon are required together"
	}
	if in.Lat != nil && (*in.Lat < -90 || *in.Lat > 90) {
		problems["lat"] = "between -90 and 90"
	}
	if in.Lon != nil && (*in.Lon < -180 || *in.Lon > 180) {
		problems["lon"] = "between -180 and 180"
	}
	if len(problems) > 0 {
		return httpx.Unprocessable("VALIDATION_FAILED", "invalid depot", problems)
	}
	return nil
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	var in depotInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(true); err != nil {
		return err
	}
	d, err := store.New(h.svc.pool).CreateDepot(r.Context(), store.CreateDepotParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, Name: *in.Name, Lat: *in.Lat, Lon: *in.Lon,
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, Depot{d.ID, d.Name, d.Lat, d.Lon, d.IsActive, d.CreatedAt})
	return nil
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := httpx.Page(r)
	if err != nil {
		return err
	}
	params := store.ListDepotsParams{OrganizationID: auth.MustPrincipal(r).OrgID, Lim: limit, Off: offset}
	switch r.URL.Query().Get("active") {
	case "true":
		params.Active = ptr(true)
	case "false":
		params.Active = ptr(false)
	}
	rows, err := store.New(h.svc.pool).ListDepots(r.Context(), params)
	if err != nil {
		return err
	}
	items := make([]Depot, len(rows))
	for i, d := range rows {
		items[i] = Depot{d.ID, d.Name, d.Lat, d.Lon, d.IsActive, d.CreatedAt}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "depot")
	if err != nil {
		return err
	}
	d, err := h.svc.Get(r.Context(), auth.MustPrincipal(r).OrgID, id)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, d)
	return nil
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "depot")
	if err != nil {
		return err
	}
	var in depotInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(false); err != nil {
		return err
	}
	d, err := store.New(h.svc.pool).UpdateDepot(r.Context(), store.UpdateDepotParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, ID: id,
		Name: in.Name, IsActive: in.IsActive, Lat: in.Lat, Lon: in.Lon,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("depot")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, Depot{d.ID, d.Name, d.Lat, d.Lon, d.IsActive, d.CreatedAt})
	return nil
}

func ptr[T any](v T) *T { return &v }
