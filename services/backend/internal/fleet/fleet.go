// Package fleet owns vehicles, drivers and the daily vehicle <-> driver pairing (spec §6).
// The solver sees one routable resource per pair (docs/ARCHITECTURE.md §5).
package fleet

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/fleet/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

var (
	vehicleStatuses = []string{"AVAILABLE", "UNAVAILABLE", "MAINTENANCE"}
	driverStatuses  = []string{"ACTIVE", "INACTIVE"}
)

type Vehicle struct {
	ID                 uuid.UUID `json:"id"`
	DepotID            uuid.UUID `json:"depot_id"`
	RegistrationNumber string    `json:"registration_number"`
	VehicleType        string    `json:"vehicle_type"`
	CapacityWeightKg   float64   `json:"capacity_weight_kg"`
	CapacityVolumeM3   float64   `json:"capacity_volume_m3"`
	CostPerKm          float64   `json:"cost_per_km"`
	FixedCost          float64   `json:"fixed_cost"`
	Status             string    `json:"status"`
	CreatedAt          time.Time `json:"created_at"`
}

type Driver struct {
	ID                   uuid.UUID `json:"id"`
	Name                 string    `json:"name"`
	Phone                *string   `json:"phone"`
	ShiftStart           string    `json:"shift_start"` // "HH:MM"
	ShiftEnd             string    `json:"shift_end"`
	BreakDurationMinutes int32     `json:"break_duration_minutes"`
	MaxOvertimeMinutes   int32     `json:"max_overtime_minutes"`
	Status               string    `json:"status"`
	CreatedAt            time.Time `json:"created_at"`
}

type Assignment struct {
	VehicleID          uuid.UUID `json:"vehicle_id"`
	RegistrationNumber string    `json:"registration_number,omitempty"`
	DriverID           uuid.UUID `json:"driver_id"`
	DriverName         string    `json:"driver_name,omitempty"`
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
	mux.Handle("POST /api/v1/vehicles", write(h.createVehicle))
	mux.Handle("GET /api/v1/vehicles", read(h.listVehicles))
	mux.Handle("GET /api/v1/vehicles/{id}", read(h.getVehicle))
	mux.Handle("PATCH /api/v1/vehicles/{id}", write(h.updateVehicle))
	mux.Handle("POST /api/v1/vehicles/{id}/unavailable", write(h.markUnavailable))

	mux.Handle("POST /api/v1/drivers", write(h.createDriver))
	mux.Handle("GET /api/v1/drivers", read(h.listDrivers))
	mux.Handle("GET /api/v1/drivers/{id}", read(h.getDriver))
	mux.Handle("PATCH /api/v1/drivers/{id}", write(h.updateDriver))

	mux.Handle("PUT /api/v1/driver-assignments/{date}", write(h.putAssignments))
	mux.Handle("GET /api/v1/driver-assignments/{date}", read(h.getAssignments))
}

// ---------------------------------------------------------------- vehicles

type vehicleInput struct {
	DepotID            *uuid.UUID `json:"depot_id"`
	RegistrationNumber *string    `json:"registration_number"`
	VehicleType        *string    `json:"vehicle_type"`
	CapacityWeightKg   *float64   `json:"capacity_weight_kg"`
	CapacityVolumeM3   *float64   `json:"capacity_volume_m3"`
	CostPerKm          *float64   `json:"cost_per_km"`
	FixedCost          *float64   `json:"fixed_cost"`
	Status             *string    `json:"status"`
}

func (in *vehicleInput) validate(create bool) error {
	p := map[string]string{}
	trim(in.RegistrationNumber)
	trim(in.VehicleType)
	required(p, create, "depot_id", in.DepotID != nil)
	required(p, create, "registration_number", in.RegistrationNumber != nil)
	required(p, create, "vehicle_type", in.VehicleType != nil)
	required(p, create, "capacity_weight_kg", in.CapacityWeightKg != nil)
	required(p, create, "capacity_volume_m3", in.CapacityVolumeM3 != nil)
	required(p, create, "cost_per_km", in.CostPerKm != nil)
	if in.RegistrationNumber != nil && (*in.RegistrationNumber == "" || len(*in.RegistrationNumber) > 32) {
		p["registration_number"] = "1-32 characters"
	}
	if in.VehicleType != nil && (*in.VehicleType == "" || len(*in.VehicleType) > 32) {
		p["vehicle_type"] = "1-32 characters"
	}
	nonNegative(p, "capacity_weight_kg", in.CapacityWeightKg)
	nonNegative(p, "capacity_volume_m3", in.CapacityVolumeM3)
	nonNegative(p, "cost_per_km", in.CostPerKm)
	nonNegative(p, "fixed_cost", in.FixedCost)
	oneOf(p, "status", in.Status, vehicleStatuses)
	if create && in.Status != nil {
		p["status"] = "set status with PATCH after creation"
	}
	return problems(p)
}

func (h *Handler) createVehicle(w http.ResponseWriter, r *http.Request) error {
	var in vehicleInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(true); err != nil {
		return err
	}
	v, err := store.New(h.pool).CreateVehicle(r.Context(), store.CreateVehicleParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, DepotID: *in.DepotID,
		RegistrationNumber: *in.RegistrationNumber, VehicleType: *in.VehicleType,
		CapacityWeightKg: *in.CapacityWeightKg, CapacityVolumeM3: *in.CapacityVolumeM3,
		CostPerKm: *in.CostPerKm, FixedCost: deref(in.FixedCost),
	})
	if err := vehicleWriteError(err); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, toVehicle(v))
	return nil
}

func (h *Handler) listVehicles(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := httpx.Page(r)
	if err != nil {
		return err
	}
	params := store.ListVehiclesParams{OrganizationID: auth.MustPrincipal(r).OrgID, Lim: limit, Off: offset}
	if v := r.URL.Query().Get("depot_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return httpx.BadRequest("INVALID_FILTER", "depot_id must be a UUID", nil)
		}
		params.DepotID = &id
	}
	if v := r.URL.Query().Get("status"); v != "" {
		params.Status = &v
	}
	rows, err := store.New(h.pool).ListVehicles(r.Context(), params)
	if err != nil {
		return err
	}
	items := make([]Vehicle, len(rows))
	for i, v := range rows {
		items[i] = toVehicle(v)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}

func (h *Handler) getVehicle(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "vehicle")
	if err != nil {
		return err
	}
	v, err := store.New(h.pool).GetVehicle(r.Context(), store.GetVehicleParams{OrganizationID: auth.MustPrincipal(r).OrgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("vehicle")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, toVehicle(v))
	return nil
}

func (h *Handler) updateVehicle(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "vehicle")
	if err != nil {
		return err
	}
	var in vehicleInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(false); err != nil {
		return err
	}
	return h.writeVehicle(w, r, store.UpdateVehicleParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, ID: id, DepotID: in.DepotID,
		RegistrationNumber: in.RegistrationNumber, VehicleType: in.VehicleType,
		CapacityWeightKg: in.CapacityWeightKg, CapacityVolumeM3: in.CapacityVolumeM3,
		CostPerKm: in.CostPerKm, FixedCost: in.FixedCost, Status: in.Status,
	})
}

// markUnavailable is POST /vehicles/{id}/unavailable (spec §12.3), e.g. after a breakdown.
func (h *Handler) markUnavailable(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "vehicle")
	if err != nil {
		return err
	}
	status := "UNAVAILABLE"
	return h.writeVehicle(w, r, store.UpdateVehicleParams{OrganizationID: auth.MustPrincipal(r).OrgID, ID: id, Status: &status})
}

func (h *Handler) writeVehicle(w http.ResponseWriter, r *http.Request, p store.UpdateVehicleParams) error {
	v, err := store.New(h.pool).UpdateVehicle(r.Context(), p)
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("vehicle")
	}
	if err := vehicleWriteError(err); err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, toVehicle(v))
	return nil
}

func vehicleWriteError(err error) error {
	switch {
	case err == nil:
		return nil
	case database.IsUniqueViolation(err, "vehicles_organization_id_registration_number_key"):
		return httpx.Conflict("REGISTRATION_TAKEN", "a vehicle with this registration number exists")
	case database.IsForeignKeyViolation(err):
		return httpx.Unprocessable("UNKNOWN_DEPOT", "depot_id does not exist in this organization", nil)
	}
	return err
}

func toVehicle(v store.OperationsVehicle) Vehicle {
	return Vehicle{v.ID, v.DepotID, v.RegistrationNumber, v.VehicleType, v.CapacityWeightKg,
		v.CapacityVolumeM3, v.CostPerKm, v.FixedCost, v.Status, v.CreatedAt}
}

// ---------------------------------------------------------------- drivers

type driverInput struct {
	Name                 *string `json:"name"`
	Phone                *string `json:"phone"`
	ShiftStart           *string `json:"shift_start"`
	ShiftEnd             *string `json:"shift_end"`
	BreakDurationMinutes *int32  `json:"break_duration_minutes"`
	MaxOvertimeMinutes   *int32  `json:"max_overtime_minutes"`
	Status               *string `json:"status"`

	start, end *pgtype.Time
}

func (in *driverInput) validate(create bool) error {
	p := map[string]string{}
	trim(in.Name)
	required(p, create, "name", in.Name != nil)
	required(p, create, "shift_start", in.ShiftStart != nil)
	required(p, create, "shift_end", in.ShiftEnd != nil)
	if in.Name != nil && (*in.Name == "" || len(*in.Name) > 200) {
		p["name"] = "1-200 characters"
	}
	var err error
	if in.ShiftStart != nil {
		if in.start, err = parseClock(*in.ShiftStart); err != nil {
			p["shift_start"] = err.Error()
		}
	}
	if in.ShiftEnd != nil {
		if in.end, err = parseClock(*in.ShiftEnd); err != nil {
			p["shift_end"] = err.Error()
		}
	}
	if in.start != nil && in.end != nil && in.end.Microseconds <= in.start.Microseconds {
		p["shift_end"] = "must be after shift_start (overnight shifts are not supported yet)"
	}
	for key, v := range map[string]*int32{"break_duration_minutes": in.BreakDurationMinutes, "max_overtime_minutes": in.MaxOvertimeMinutes} {
		if v != nil && (*v < 0 || *v > 600) {
			p[key] = "between 0 and 600"
		}
	}
	oneOf(p, "status", in.Status, driverStatuses)
	return problems(p)
}

func (h *Handler) createDriver(w http.ResponseWriter, r *http.Request) error {
	var in driverInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(true); err != nil {
		return err
	}
	d, err := store.New(h.pool).CreateDriver(r.Context(), store.CreateDriverParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, Name: *in.Name, Phone: in.Phone,
		ShiftStart: *in.start, ShiftEnd: *in.end,
		BreakDurationMinutes: deref(in.BreakDurationMinutes), MaxOvertimeMinutes: deref(in.MaxOvertimeMinutes),
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, toDriver(d))
	return nil
}

func (h *Handler) listDrivers(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := httpx.Page(r)
	if err != nil {
		return err
	}
	params := store.ListDriversParams{OrganizationID: auth.MustPrincipal(r).OrgID, Lim: limit, Off: offset}
	if v := r.URL.Query().Get("status"); v != "" {
		params.Status = &v
	}
	rows, err := store.New(h.pool).ListDrivers(r.Context(), params)
	if err != nil {
		return err
	}
	items := make([]Driver, len(rows))
	for i, d := range rows {
		items[i] = toDriver(d)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}

func (h *Handler) getDriver(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "driver")
	if err != nil {
		return err
	}
	d, err := store.New(h.pool).GetDriver(r.Context(), store.GetDriverParams{OrganizationID: auth.MustPrincipal(r).OrgID, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("driver")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, toDriver(d))
	return nil
}

func (h *Handler) updateDriver(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "driver")
	if err != nil {
		return err
	}
	var in driverInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := in.validate(false); err != nil {
		return err
	}
	d, err := store.New(h.pool).UpdateDriver(r.Context(), store.UpdateDriverParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, ID: id, Name: in.Name, Phone: in.Phone,
		ShiftStart: deref(in.start), ShiftEnd: deref(in.end), BreakDurationMinutes: in.BreakDurationMinutes,
		MaxOvertimeMinutes: in.MaxOvertimeMinutes, Status: in.Status,
	})
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		return httpx.NotFound("driver")
	case database.IsCheckViolation(err): // e.g. new shift_end before the stored shift_start
		return httpx.Unprocessable("VALIDATION_FAILED", "shift_end must be after shift_start", nil)
	case err != nil:
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, toDriver(d))
	return nil
}

func toDriver(d store.OperationsDriver) Driver {
	return Driver{d.ID, d.Name, d.Phone, formatClock(d.ShiftStart), formatClock(d.ShiftEnd),
		d.BreakDurationMinutes, d.MaxOvertimeMinutes, d.Status, d.CreatedAt}
}

// ---------------------------------------------------------------- daily pairing

// putAssignments replaces the whole pairing for one date in a single transaction.
func (h *Handler) putAssignments(w http.ResponseWriter, r *http.Request) error {
	date, err := time.Parse(time.DateOnly, r.PathValue("date"))
	if err != nil {
		return httpx.BadRequest("INVALID_DATE", "date must be YYYY-MM-DD", nil)
	}
	var in struct {
		Assignments []Assignment `json:"assignments"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	org := auth.MustPrincipal(r).OrgID
	day := pgtype.Date{Time: date, Valid: true}
	err = database.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		if err := q.DeleteAssignmentsForDate(r.Context(), store.DeleteAssignmentsForDateParams{OrganizationID: org, PlanningDate: day}); err != nil {
			return err
		}
		for i, a := range in.Assignments {
			err := q.InsertAssignment(r.Context(), store.InsertAssignmentParams{
				OrganizationID: org, PlanningDate: day, VehicleID: a.VehicleID, DriverID: a.DriverID,
			})
			switch {
			case database.IsUniqueViolation(err, ""):
				return httpx.Unprocessable("DOUBLE_BOOKED",
					fmt.Sprintf("assignment %d: vehicle or driver is already paired on this date", i), nil)
			case database.IsForeignKeyViolation(err):
				return httpx.Unprocessable("UNKNOWN_RESOURCE",
					fmt.Sprintf("assignment %d: vehicle or driver does not exist in this organization", i), nil)
			case err != nil:
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return h.getAssignments(w, r)
}

func (h *Handler) getAssignments(w http.ResponseWriter, r *http.Request) error {
	date, err := time.Parse(time.DateOnly, r.PathValue("date"))
	if err != nil {
		return httpx.BadRequest("INVALID_DATE", "date must be YYYY-MM-DD", nil)
	}
	rows, err := store.New(h.pool).ListAssignments(r.Context(), store.ListAssignmentsParams{
		OrganizationID: auth.MustPrincipal(r).OrgID, PlanningDate: pgtype.Date{Time: date, Valid: true},
	})
	if err != nil {
		return err
	}
	items := make([]Assignment, len(rows))
	for i, a := range rows {
		items[i] = Assignment{a.VehicleID, a.RegistrationNumber, a.DriverID, a.DriverName}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"date": r.PathValue("date"), "assignments": items})
	return nil
}

// ---------------------------------------------------------------- helpers

func parseClock(s string) (*pgtype.Time, error) {
	t, err := time.Parse("15:04", strings.TrimSpace(s))
	if err != nil {
		return nil, errors.New("HH:MM (24h)")
	}
	us := int64(t.Hour()*3600+t.Minute()*60) * 1_000_000
	return &pgtype.Time{Microseconds: us, Valid: true}, nil
}

func formatClock(t pgtype.Time) string {
	mins := t.Microseconds / 60_000_000
	return fmt.Sprintf("%02d:%02d", mins/60, mins%60)
}

func trim(s *string) {
	if s != nil {
		*s = strings.TrimSpace(*s)
	}
}

func required(p map[string]string, create bool, key string, present bool) {
	if create && !present {
		p[key] = "required"
	}
}

func nonNegative(p map[string]string, key string, v *float64) {
	if v != nil && *v < 0 {
		p[key] = "must be >= 0"
	}
}

func oneOf(p map[string]string, key string, v *string, allowed []string) {
	if v == nil {
		return
	}
	*v = strings.ToUpper(strings.TrimSpace(*v))
	for _, a := range allowed {
		if *v == a {
			return
		}
	}
	p[key] = "one of " + strings.Join(allowed, ", ")
}

func problems(p map[string]string) error {
	if len(p) > 0 {
		return httpx.Unprocessable("VALIDATION_FAILED", "invalid input", p)
	}
	return nil
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
