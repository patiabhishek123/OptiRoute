package fleet

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/fleet/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/csvx"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

// Both imports are all-or-nothing like the orders import: every row is validated first and
// nothing is written unless all rows are valid. A natural key that already exists (registration
// number, driver name; case-insensitive) is an error unless ?skip_existing=true.

var vehicleColumns = map[string][]string{
	"registration_number": {"registration_number", "registration", "reg_no", "vehicle"},
	"depot":               {"depot", "depot_id", "depot_name"},
	"vehicle_type":        {"vehicle_type", "type"},
	"capacity_weight_kg":  {"capacity_weight_kg", "capacity_kg", "weight_kg"},
	"capacity_volume_m3":  {"capacity_volume_m3", "capacity_m3", "volume_m3"},
	"cost_per_km":         {"cost_per_km"},
	"fixed_cost":          {"fixed_cost", "fixed_cost_per_day"},
}

var driverColumns = map[string][]string{
	"name":                   {"name", "driver", "driver_name"},
	"phone":                  {"phone"},
	"shift_start":            {"shift_start"},
	"shift_end":              {"shift_end"},
	"break_duration_minutes": {"break_duration_minutes", "break_minutes", "break"},
	"max_overtime_minutes":   {"max_overtime_minutes", "overtime_minutes", "max_overtime"},
}

func (h *Handler) importVehicles(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	skipExisting := r.URL.Query().Get("skip_existing") == "true"
	body, err := csvx.Body(w, r)
	if err != nil {
		return err
	}
	rows, err := csvx.Parse(body, vehicleColumns,
		[]string{"registration_number", "depot", "vehicle_type", "capacity_weight_kg", "capacity_volume_m3", "cost_per_km"})
	if err != nil {
		return err
	}
	exists, err := h.existingKeys(r.Context(), org, true)
	if err != nil {
		return err
	}

	var toInsert []store.CreateVehicleParams
	firstLine := map[string]int{}
	depotCache := map[string]uuid.UUID{}
	for _, row := range rows {
		in := vehicleInput{
			RegistrationNumber: row.String("registration_number"), VehicleType: row.String("vehicle_type"),
			CapacityWeightKg: row.Float("capacity_weight_kg"), CapacityVolumeM3: row.Float("capacity_volume_m3"),
			CostPerKm: row.Float("cost_per_km"), FixedCost: row.Float("fixed_cost"),
		}
		if ref := row.Get("depot"); ref == "" {
			row.Problems(map[string]string{"depot": "required"})
		} else if id, ok := depotCache[csvx.Key(ref)]; ok {
			in.DepotID = &id
		} else if id, err := h.depots.ResolveDepot(r.Context(), org, ref); err != nil {
			var he *httpx.Error
			if !errors.As(err, &he) {
				return err
			}
			row.Problems(map[string]string{"depot": he.Message})
		} else {
			depotCache[csvx.Key(ref)], in.DepotID = id, &id
		}
		problems := in.check(true)
		delete(problems, "depot_id") // reported as "depot" above
		row.Problems(problems)

		if in.RegistrationNumber == nil || *in.RegistrationNumber == "" {
			continue
		}
		key := csvx.Key(*in.RegistrationNumber)
		if first, dup := firstLine[key]; dup {
			row.Problems(map[string]string{"registration_number": fmt.Sprintf("duplicate of line %d", first)})
			continue
		}
		firstLine[key] = row.Line
		switch {
		case exists[key] && skipExisting:
		case exists[key]:
			row.Problems(map[string]string{"registration_number": "a vehicle with this registration exists (use skip_existing)"})
		case len(row.Errs) == 0:
			toInsert = append(toInsert, store.CreateVehicleParams{
				OrganizationID: org, DepotID: *in.DepotID,
				RegistrationNumber: *in.RegistrationNumber, VehicleType: *in.VehicleType,
				CapacityWeightKg: *in.CapacityWeightKg, CapacityVolumeM3: *in.CapacityVolumeM3,
				CostPerKm: *in.CostPerKm, FixedCost: deref(in.FixedCost),
			})
		}
	}
	if errs := csvx.Errors(rows); len(errs) > 0 {
		return csvx.Rejected(errs, len(rows))
	}

	err = database.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		for _, p := range toInsert {
			if _, err := store.New(tx).CreateVehicle(r.Context(), p); err != nil {
				return err
			}
		}
		return nil
	})
	if database.IsUniqueViolation(err, "") {
		return httpx.Conflict("CONCURRENT_IMPORT", "some vehicles were created concurrently; retry with skip_existing=true")
	} else if err != nil {
		return err
	}
	return imported(w, len(toInsert), len(rows))
}

func (h *Handler) importDrivers(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	skipExisting := r.URL.Query().Get("skip_existing") == "true"
	body, err := csvx.Body(w, r)
	if err != nil {
		return err
	}
	rows, err := csvx.Parse(body, driverColumns, []string{"name", "shift_start", "shift_end"})
	if err != nil {
		return err
	}
	exists, err := h.existingKeys(r.Context(), org, false)
	if err != nil {
		return err
	}

	var toInsert []store.CreateDriverParams
	firstLine := map[string]int{}
	for _, row := range rows {
		in := driverInput{
			Name: row.String("name"), Phone: row.String("phone"),
			ShiftStart: row.String("shift_start"), ShiftEnd: row.String("shift_end"),
			BreakDurationMinutes: row.Int32("break_duration_minutes"), MaxOvertimeMinutes: row.Int32("max_overtime_minutes"),
		}
		row.Problems(in.check(true))
		if in.Name == nil || *in.Name == "" {
			continue
		}
		key := csvx.Key(*in.Name)
		if first, dup := firstLine[key]; dup {
			row.Problems(map[string]string{"name": fmt.Sprintf("duplicate of line %d", first)})
			continue
		}
		firstLine[key] = row.Line
		switch {
		case exists[key] && skipExisting:
		case exists[key]:
			row.Problems(map[string]string{"name": "a driver with this name exists (use skip_existing)"})
		case len(row.Errs) == 0:
			toInsert = append(toInsert, store.CreateDriverParams{
				OrganizationID: org, Name: *in.Name, Phone: in.Phone, ShiftStart: *in.start, ShiftEnd: *in.end,
				BreakDurationMinutes: deref(in.BreakDurationMinutes), MaxOvertimeMinutes: deref(in.MaxOvertimeMinutes),
			})
		}
	}
	if errs := csvx.Errors(rows); len(errs) > 0 {
		return csvx.Rejected(errs, len(rows))
	}

	err = database.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		for _, p := range toInsert {
			if _, err := store.New(tx).CreateDriver(r.Context(), p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	return imported(w, len(toInsert), len(rows))
}

// existingKeys returns the organization's normalized vehicle registrations or driver names.
func (h *Handler) existingKeys(ctx context.Context, org uuid.UUID, vehicles bool) (map[string]bool, error) {
	const page = 1000
	keys := map[string]bool{}
	q := store.New(h.pool)
	for off := int32(0); ; off += page {
		var n int
		if vehicles {
			rows, err := q.ListVehicles(ctx, store.ListVehiclesParams{OrganizationID: org, Lim: page, Off: off})
			if err != nil {
				return nil, err
			}
			for _, v := range rows {
				keys[csvx.Key(v.RegistrationNumber)] = true
			}
			n = len(rows)
		} else {
			rows, err := q.ListDrivers(ctx, store.ListDriversParams{OrganizationID: org, Lim: page, Off: off})
			if err != nil {
				return nil, err
			}
			for _, d := range rows {
				keys[csvx.Key(d.Name)] = true
			}
			n = len(rows)
		}
		if n < page {
			return keys, nil
		}
	}
}

func imported(w http.ResponseWriter, n, rows int) error {
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"imported": n, "skipped_existing": rows - n, "rows": rows})
	return nil
}
