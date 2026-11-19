// Package orders owns delivery orders: creation, CSV import, editing and lifecycle (spec §6).
package orders

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/orders/store"
)

// DepotResolver turns a depot reference (UUID or name) into an id. Implemented by geography;
// orders never reads depot tables itself (spec §6.1: modules interact through interfaces).
type DepotResolver interface {
	ResolveDepot(ctx context.Context, org uuid.UUID, ref string) (uuid.UUID, error)
}

type Order struct {
	ID                     uuid.UUID  `json:"id"`
	ExternalOrderID        string     `json:"external_order_id"`
	DepotID                *uuid.UUID `json:"depot_id"`
	PlanningDate           string     `json:"planning_date"`
	BatchID                *string    `json:"batch_id"`
	CustomerName           string     `json:"customer_name"`
	CustomerPhone          *string    `json:"customer_phone"`
	Lat                    float64    `json:"lat"`
	Lon                    float64    `json:"lon"`
	DemandWeightKg         float64    `json:"demand_weight_kg"`
	DemandVolumeM3         float64    `json:"demand_volume_m3"`
	TimeWindowStart        time.Time  `json:"time_window_start"`
	TimeWindowEnd          time.Time  `json:"time_window_end"`
	ServiceDurationMinutes int32      `json:"service_duration_minutes"`
	Priority               int32      `json:"priority"`
	Status                 string     `json:"status"`
	CreatedAt              time.Time  `json:"created_at"`
}

func toOrder(o store.GetOrderRow) Order {
	return Order{o.ID, o.ExternalOrderID, o.DepotID, o.PlanningDate.Time.Format(time.DateOnly), o.BatchID,
		o.CustomerName, o.CustomerPhone, o.Lat, o.Lon, o.DemandWeightKg, o.DemandVolumeM3,
		o.TimeWindowStart, o.TimeWindowEnd, o.ServiceDurationMinutes, o.Priority, o.Status, o.CreatedAt}
}

// Input is one order as submitted by JSON or a CSV row. Time windows accept RFC 3339 or "HH:MM";
// the latter is read in the organization's timezone on the planning date.
type Input struct {
	ExternalOrderID        string   `json:"external_order_id"`
	Depot                  *string  `json:"depot"` // UUID or depot name
	PlanningDate           string   `json:"planning_date"`
	BatchID                *string  `json:"batch_id"`
	CustomerName           string   `json:"customer_name"`
	CustomerPhone          *string  `json:"customer_phone"`
	Lat                    *float64 `json:"lat"`
	Lon                    *float64 `json:"lon"`
	DemandWeightKg         *float64 `json:"demand_weight_kg"`
	DemandVolumeM3         *float64 `json:"demand_volume_m3"`
	TimeWindowStart        string   `json:"time_window_start"`
	TimeWindowEnd          string   `json:"time_window_end"`
	ServiceDurationMinutes *int32   `json:"service_duration_minutes"`
	Priority               *int32   `json:"priority"`
}

// validate checks an Input and builds insert parameters. Depot is resolved separately.
// problems maps field -> message; it is empty when the input is valid.
func (in Input) validate(org uuid.UUID, tz *time.Location) (store.InsertOrderBatchParams, map[string]string) {
	p := map[string]string{}
	out := store.InsertOrderBatchParams{
		OrganizationID:         org,
		ExternalOrderID:        strings.TrimSpace(in.ExternalOrderID),
		CustomerName:           strings.TrimSpace(in.CustomerName),
		CustomerPhone:          blankToNil(in.CustomerPhone),
		BatchID:                blankToNil(in.BatchID),
		ServiceDurationMinutes: 5,
		Priority:               1,
	}
	if n := len(out.ExternalOrderID); n == 0 || n > 64 {
		p["external_order_id"] = "1-64 characters"
	}
	if n := len(out.CustomerName); n == 0 || n > 200 {
		p["customer_name"] = "1-200 characters"
	}
	day, err := time.ParseInLocation(time.DateOnly, strings.TrimSpace(in.PlanningDate), tz)
	if err != nil {
		p["planning_date"] = "YYYY-MM-DD"
	}
	out.PlanningDate = pgtype.Date{Time: day, Valid: err == nil}

	switch {
	case in.Lat == nil || in.Lon == nil:
		p["location"] = "lat and lon are required"
	case *in.Lat < -90 || *in.Lat > 90:
		p["lat"] = "between -90 and 90"
	case *in.Lon < -180 || *in.Lon > 180:
		p["lon"] = "between -180 and 180"
	default:
		out.Lat, out.Lon = *in.Lat, *in.Lon
	}
	for key, v := range map[string]*float64{"demand_weight_kg": in.DemandWeightKg, "demand_volume_m3": in.DemandVolumeM3} {
		if v != nil && *v < 0 {
			p[key] = "must be >= 0"
		}
	}
	out.DemandWeightKg, out.DemandVolumeM3 = deref(in.DemandWeightKg), deref(in.DemandVolumeM3)
	if v := in.ServiceDurationMinutes; v != nil {
		if *v < 0 || *v > 600 {
			p["service_duration_minutes"] = "between 0 and 600"
		}
		out.ServiceDurationMinutes = *v
	}
	if v := in.Priority; v != nil {
		if *v < 1 || *v > 10 {
			p["priority"] = "between 1 and 10"
		}
		out.Priority = *v
	}

	if _, bad := p["planning_date"]; !bad {
		var e1, e2 error
		out.TimeWindowStart, e1 = parseWindow(in.TimeWindowStart, day, tz)
		out.TimeWindowEnd, e2 = parseWindow(in.TimeWindowEnd, day, tz)
		if e1 != nil {
			p["time_window_start"] = e1.Error()
		}
		if e2 != nil {
			p["time_window_end"] = e2.Error()
		}
		if e1 == nil && e2 == nil && out.TimeWindowEnd.Before(out.TimeWindowStart) {
			p["time_window_end"] = "must not be before time_window_start"
		}
	}
	return out, p
}

// parseWindow reads RFC 3339, or "HH:MM" on the planning day in the organization's timezone.
func parseWindow(s string, day time.Time, tz *time.Location) (time.Time, error) {
	s = strings.TrimSpace(s)
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t, nil
	}
	clock, err := time.Parse("15:04", s)
	if err != nil {
		return time.Time{}, fmt.Errorf("RFC 3339 timestamp or HH:MM")
	}
	return time.Date(day.Year(), day.Month(), day.Day(), clock.Hour(), clock.Minute(), 0, 0, tz), nil
}

func blankToNil(s *string) *string {
	if s == nil || strings.TrimSpace(*s) == "" {
		return nil
	}
	v := strings.TrimSpace(*s)
	return &v
}

func deref[T any](p *T) T {
	var zero T
	if p == nil {
		return zero
	}
	return *p
}
