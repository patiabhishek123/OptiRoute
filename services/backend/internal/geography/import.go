package geography

import (
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/geography/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/csvx"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

var depotColumns = map[string][]string{
	"name": {"name", "depot", "depot_name"},
	"lat":  {"lat", "latitude"},
	"lon":  {"lon", "lng", "long", "longitude"},
}

// bulkImport creates depots from CSV (name, lat, lon). It is all-or-nothing like the orders
// import. Names are the key orders and vehicles use to refer to a depot, so a name that already
// exists (case-insensitive) is an error unless ?skip_existing=true.
func (h *Handler) bulkImport(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	skipExisting := r.URL.Query().Get("skip_existing") == "true"
	body, err := csvx.Body(w, r)
	if err != nil {
		return err
	}
	rows, err := csvx.Parse(body, depotColumns, []string{"name", "lat", "lon"})
	if err != nil {
		return err
	}

	q := store.New(h.svc.pool)
	var toInsert []store.CreateDepotParams
	firstLine := map[string]int{}
	for _, row := range rows {
		in := depotInput{Name: row.String("name"), Lat: row.Float("lat"), Lon: row.Float("lon")}
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
		existing, err := q.FindDepotByName(r.Context(), store.FindDepotByNameParams{OrganizationID: org, Name: *in.Name})
		if err != nil {
			return err
		}
		switch {
		case len(existing) > 0 && skipExisting:
		case len(existing) > 0:
			row.Problems(map[string]string{"name": "a depot with this name exists (use skip_existing)"})
		case len(row.Errs) == 0:
			toInsert = append(toInsert, store.CreateDepotParams{OrganizationID: org, Name: *in.Name, Lat: *in.Lat, Lon: *in.Lon})
		}
	}
	if errs := csvx.Errors(rows); len(errs) > 0 {
		return csvx.Rejected(errs, len(rows))
	}

	err = database.WithTx(r.Context(), h.svc.pool, func(tx pgx.Tx) error {
		for _, p := range toInsert {
			if _, err := store.New(tx).CreateDepot(r.Context(), p); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"imported": len(toInsert), "skipped_existing": len(rows) - len(toInsert), "rows": len(rows),
	})
	return nil
}
