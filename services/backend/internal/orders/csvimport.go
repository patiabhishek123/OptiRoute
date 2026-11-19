package orders

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

const (
	maxImportRows   = 10_000
	maxImportBytes  = 5 << 20
	maxReportErrors = 100
)

// RowError points at one bad cell. Row is the 1-based line number in the file (header = 1).
type RowError struct {
	Row     int    `json:"row"`
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Accepted header names (case-insensitive) for each Input field.
var columnAliases = map[string][]string{
	"external_order_id":        {"external_order_id", "order_id", "order", "id"},
	"depot":                    {"depot", "depot_id", "depot_name"},
	"planning_date":            {"planning_date", "date"},
	"batch_id":                 {"batch_id", "batch"},
	"customer_name":            {"customer_name", "customer", "name"},
	"customer_phone":           {"customer_phone", "phone"},
	"lat":                      {"lat", "latitude"},
	"lon":                      {"lon", "lng", "long", "longitude"},
	"demand_weight_kg":         {"demand_weight_kg", "weight_kg", "weight"},
	"demand_volume_m3":         {"demand_volume_m3", "volume_m3", "volume"},
	"time_window_start":        {"time_window_start", "window_start", "tw_start", "from"},
	"time_window_end":          {"time_window_end", "window_end", "tw_end", "to"},
	"service_duration_minutes": {"service_duration_minutes", "service_minutes", "service"},
	"priority":                 {"priority"},
}

var requiredColumns = []string{"external_order_id", "customer_name", "lat", "lon", "time_window_start", "time_window_end"}

// csvRow is a parsed CSV line: the Input plus its line number, or the cells that failed parsing.
type csvRow struct {
	line  int
	input Input
	errs  []RowError
}

// parseCSV reads the file into Inputs. Defaults (from query parameters) fill columns that are
// absent or empty: planning_date and batch_id. Header problems are returned as a single error.
func parseCSV(r io.Reader, defaultDate, defaultBatch string) ([]csvRow, error) {
	cr := csv.NewReader(r)
	cr.TrimLeadingSpace = true
	cr.ReuseRecord = false
	header, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return nil, errors.New("the file is empty")
	} else if err != nil {
		return nil, fmt.Errorf("unreadable CSV header: %w", err)
	}

	col := map[string]int{} // field -> column index
	for i, h := range header {
		h = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(h, "\ufeff"))) // Excel BOM
		for field, aliases := range columnAliases {
			for _, a := range aliases {
				if h == a {
					if _, dup := col[field]; dup {
						return nil, fmt.Errorf("column for %q appears twice", field)
					}
					col[field] = i
				}
			}
		}
	}
	var missing []string
	for _, f := range requiredColumns {
		if _, ok := col[f]; !ok {
			missing = append(missing, f)
		}
	}
	if _, ok := col["planning_date"]; !ok && defaultDate == "" {
		missing = append(missing, "planning_date (column or ?planning_date=)")
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("missing required columns: %s", strings.Join(missing, ", "))
	}

	var rows []csvRow
	for {
		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}
		line, _ := cr.FieldPos(0)
		if err != nil {
			rows = append(rows, csvRow{line: line, errs: []RowError{{line, "", err.Error()}}})
			continue
		}
		if len(rows) >= maxImportRows {
			return nil, fmt.Errorf("more than %d rows; split the file", maxImportRows)
		}
		cell := func(field string) string {
			if i, ok := col[field]; ok && i < len(rec) {
				return strings.TrimSpace(rec[i])
			}
			return ""
		}
		row := csvRow{line: line}
		num := func(field string) *float64 {
			v := cell(field)
			if v == "" {
				return nil
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil {
				row.errs = append(row.errs, RowError{line, field, "not a number"})
				return nil
			}
			return &f
		}
		integer := func(field string) *int32 {
			v := cell(field)
			if v == "" {
				return nil
			}
			n, err := strconv.ParseInt(v, 10, 32)
			if err != nil {
				row.errs = append(row.errs, RowError{line, field, "not an integer"})
				return nil
			}
			n32 := int32(n)
			return &n32
		}
		optional := func(field, fallback string) *string {
			if v := cell(field); v != "" {
				return &v
			}
			if fallback != "" {
				return &fallback
			}
			return nil
		}
		date := cell("planning_date")
		if date == "" {
			date = defaultDate
		}
		row.input = Input{
			ExternalOrderID:        cell("external_order_id"),
			Depot:                  optional("depot", ""),
			PlanningDate:           date,
			BatchID:                optional("batch_id", defaultBatch),
			CustomerName:           cell("customer_name"),
			CustomerPhone:          optional("customer_phone", ""),
			Lat:                    num("lat"),
			Lon:                    num("lon"),
			DemandWeightKg:         num("demand_weight_kg"),
			DemandVolumeM3:         num("demand_volume_m3"),
			TimeWindowStart:        cell("time_window_start"),
			TimeWindowEnd:          cell("time_window_end"),
			ServiceDurationMinutes: integer("service_duration_minutes"),
			Priority:               integer("priority"),
		}
		rows = append(rows, row)
	}
	if len(rows) == 0 {
		return nil, errors.New("the file has a header but no rows")
	}
	return rows, nil
}
