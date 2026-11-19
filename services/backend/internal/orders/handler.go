package orders

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"mime"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/orders/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

var statuses = []string{"PENDING", "PLANNED", "DISPATCHED", "DELIVERED", "FAILED", "CANCELLED"}

type Handler struct {
	pool   *pgxpool.Pool
	depots DepotResolver
	log    *slog.Logger
}

func NewHandler(pool *pgxpool.Pool, depots DepotResolver, log *slog.Logger) *Handler {
	return &Handler{pool, depots, log}
}

func (h *Handler) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	read := func(f httpx.HandlerFunc) http.Handler { return authn(httpx.Handle(h.log, f)) }
	write := func(f httpx.HandlerFunc) http.Handler {
		return authn(auth.RequireRole(httpx.Handle(h.log, f), auth.Writers...))
	}
	mux.Handle("POST /api/v1/orders", write(h.create))
	mux.Handle("POST /api/v1/orders/bulk-import", write(h.bulkImport))
	mux.Handle("GET /api/v1/orders", read(h.list))
	mux.Handle("GET /api/v1/orders/{id}", read(h.get))
	mux.Handle("PATCH /api/v1/orders/{id}", write(h.update))
	mux.Handle("POST /api/v1/orders/{id}/cancel", write(h.cancel))
}

func (h *Handler) timezone(ctx context.Context, org uuid.UUID) (*time.Location, error) {
	name, err := store.New(h.pool).OrganizationTimezone(ctx, org)
	if err != nil {
		return nil, err
	}
	return time.LoadLocation(name)
}

// resolveDepot maps an Input's depot reference to an id (nil when no depot is given).
func (h *Handler) resolveDepot(ctx context.Context, org uuid.UUID, ref *string) (*uuid.UUID, error) {
	if ref == nil || strings.TrimSpace(*ref) == "" {
		return nil, nil
	}
	id, err := h.depots.ResolveDepot(ctx, org, *ref)
	if err != nil {
		var he *httpx.Error
		if errors.As(err, &he) {
			return nil, errors.New(he.Message)
		}
		return nil, err
	}
	return &id, nil
}

// ---------------------------------------------------------------- single order

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	var in Input
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	tz, err := h.timezone(r.Context(), org)
	if err != nil {
		return err
	}
	params, problems := in.validate(org, tz)
	depot, derr := h.resolveDepot(r.Context(), org, in.Depot)
	if derr != nil {
		problems["depot"] = derr.Error()
	}
	if len(problems) > 0 {
		return httpx.Unprocessable("VALIDATION_FAILED", "invalid order", problems)
	}
	params.DepotID = depot
	id, err := store.New(h.pool).CreateOrder(r.Context(), store.CreateOrderParams(params))
	if database.IsUniqueViolation(err, "orders_organization_id_external_order_id_key") {
		return httpx.Conflict("DUPLICATE_ORDER", "an order with this external_order_id exists")
	} else if err != nil {
		return err
	}
	return h.writeOrder(w, r, org, id, http.StatusCreated)
}

func (h *Handler) writeOrder(w http.ResponseWriter, r *http.Request, org, id uuid.UUID, status int) error {
	o, err := store.New(h.pool).GetOrder(r.Context(), store.GetOrderParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("order")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, status, toOrder(o))
	return nil
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := httpx.PathUUID(r, "id", "order")
	if err != nil {
		return err
	}
	return h.writeOrder(w, r, auth.MustPrincipal(r).OrgID, id, http.StatusOK)
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, offset, err := httpx.Page(r)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	params := store.ListOrdersParams{OrganizationID: auth.MustPrincipal(r).OrgID, Lim: limit, Off: offset}
	if v := q.Get("planning_date"); v != "" {
		d, err := time.Parse(time.DateOnly, v)
		if err != nil {
			return httpx.BadRequest("INVALID_FILTER", "planning_date must be YYYY-MM-DD", nil)
		}
		params.PlanningDate = pgtype.Date{Time: d, Valid: true}
	}
	if v := strings.ToUpper(q.Get("status")); v != "" {
		params.Status = &v
	}
	if v := q.Get("depot_id"); v != "" {
		id, err := uuid.Parse(v)
		if err != nil {
			return httpx.BadRequest("INVALID_FILTER", "depot_id must be a UUID", nil)
		}
		params.DepotID = &id
	}
	if v := q.Get("batch_id"); v != "" {
		params.BatchID = &v
	}
	rows, err := store.New(h.pool).ListOrders(r.Context(), params)
	if err != nil {
		return err
	}
	items := make([]Order, len(rows))
	for i, o := range rows {
		items[i] = toOrder(store.GetOrderRow(o))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": items, "limit": limit, "offset": offset})
	return nil
}

// update edits a PENDING order. Fields are optional; time windows use the stored planning date.
func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "order")
	if err != nil {
		return err
	}
	var in struct {
		Depot                  *string  `json:"depot"`
		CustomerName           *string  `json:"customer_name"`
		CustomerPhone          *string  `json:"customer_phone"`
		Lat                    *float64 `json:"lat"`
		Lon                    *float64 `json:"lon"`
		DemandWeightKg         *float64 `json:"demand_weight_kg"`
		DemandVolumeM3         *float64 `json:"demand_volume_m3"`
		TimeWindowStart        *string  `json:"time_window_start"`
		TimeWindowEnd          *string  `json:"time_window_end"`
		ServiceDurationMinutes *int32   `json:"service_duration_minutes"`
		Priority               *int32   `json:"priority"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	q := store.New(h.pool)
	cur, err := q.GetOrder(r.Context(), store.GetOrderParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		return httpx.NotFound("order")
	} else if err != nil {
		return err
	}
	if cur.Status != "PENDING" {
		return httpx.Conflict("ORDER_NOT_EDITABLE", "only PENDING orders can be edited; this one is "+cur.Status)
	}
	tz, err := h.timezone(r.Context(), org)
	if err != nil {
		return err
	}

	// Validate the merged order with the same rules as creation.
	merged := Input{
		ExternalOrderID: cur.ExternalOrderID, PlanningDate: cur.PlanningDate.Time.Format(time.DateOnly),
		CustomerName: or(in.CustomerName, cur.CustomerName), CustomerPhone: in.CustomerPhone,
		Lat: orPtr(in.Lat, cur.Lat), Lon: orPtr(in.Lon, cur.Lon),
		DemandWeightKg: orPtr(in.DemandWeightKg, cur.DemandWeightKg), DemandVolumeM3: orPtr(in.DemandVolumeM3, cur.DemandVolumeM3),
		TimeWindowStart:        or(in.TimeWindowStart, cur.TimeWindowStart.Format(time.RFC3339)),
		TimeWindowEnd:          or(in.TimeWindowEnd, cur.TimeWindowEnd.Format(time.RFC3339)),
		ServiceDurationMinutes: orPtr(in.ServiceDurationMinutes, cur.ServiceDurationMinutes),
		Priority:               orPtr(in.Priority, cur.Priority),
	}
	if (in.Lat == nil) != (in.Lon == nil) {
		return httpx.Unprocessable("VALIDATION_FAILED", "invalid order", map[string]string{"location": "lat and lon are required together"})
	}
	v, problems := merged.validate(org, tz)
	depot, derr := h.resolveDepot(r.Context(), org, in.Depot)
	if derr != nil {
		problems["depot"] = derr.Error()
	}
	if len(problems) > 0 {
		return httpx.Unprocessable("VALIDATION_FAILED", "invalid order", problems)
	}
	params := store.UpdatePendingOrderParams{
		OrganizationID: org, ID: id, DepotID: depot, CustomerName: in.CustomerName, CustomerPhone: blankToNil(in.CustomerPhone),
		Lat: in.Lat, Lon: in.Lon, DemandWeightKg: in.DemandWeightKg, DemandVolumeM3: in.DemandVolumeM3,
		ServiceDurationMinutes: in.ServiceDurationMinutes, Priority: in.Priority,
	}
	if in.TimeWindowStart != nil {
		params.TimeWindowStart = &v.TimeWindowStart
	}
	if in.TimeWindowEnd != nil {
		params.TimeWindowEnd = &v.TimeWindowEnd
	}
	if _, err := q.UpdatePendingOrder(r.Context(), params); errors.Is(err, pgx.ErrNoRows) {
		return httpx.Conflict("ORDER_NOT_EDITABLE", "the order was planned or cancelled meanwhile")
	} else if err != nil {
		return err
	}
	return h.writeOrder(w, r, org, id, http.StatusOK)
}

// cancel is allowed while PENDING or PLANNED; dispatched or finished orders are history.
func (h *Handler) cancel(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	id, err := httpx.PathUUID(r, "id", "order")
	if err != nil {
		return err
	}
	q := store.New(h.pool)
	_, err = q.CancelOrder(r.Context(), store.CancelOrderParams{OrganizationID: org, ID: id})
	if errors.Is(err, pgx.ErrNoRows) {
		status, err := q.OrderStatus(r.Context(), store.OrderStatusParams{OrganizationID: org, ID: id})
		if errors.Is(err, pgx.ErrNoRows) {
			return httpx.NotFound("order")
		} else if err != nil {
			return err
		}
		return httpx.Conflict("ORDER_NOT_CANCELLABLE", "an order that is "+status+" cannot be cancelled")
	} else if err != nil {
		return err
	}
	return h.writeOrder(w, r, org, id, http.StatusOK)
}

// ---------------------------------------------------------------- CSV import

// bulkImport is all-or-nothing: every row is validated first and nothing is written unless all
// rows are valid. ?skip_existing=true makes re-uploading the same file safe.
func (h *Handler) bulkImport(w http.ResponseWriter, r *http.Request) error {
	org := auth.MustPrincipal(r).OrgID
	q := r.URL.Query()
	skipExisting := q.Get("skip_existing") == "true"

	body, err := csvBody(w, r)
	if err != nil {
		return err
	}
	rows, err := parseCSV(body, q.Get("planning_date"), q.Get("batch_id"))
	if err != nil {
		return httpx.BadRequest("INVALID_CSV", err.Error(), nil)
	}
	tz, err := h.timezone(r.Context(), org)
	if err != nil {
		return err
	}

	var errs []RowError
	params := make([]store.InsertOrderBatchParams, 0, len(rows))
	lineOf := make(map[string]int, len(rows))
	depotCache := map[string]*uuid.UUID{}
	for _, row := range rows {
		errs = append(errs, row.errs...)
		p, problems := row.input.validate(org, tz)
		if ref := row.input.Depot; ref != nil {
			key := strings.ToLower(*ref)
			if id, ok := depotCache[key]; ok {
				p.DepotID = id
			} else if id, err := h.resolveDepot(r.Context(), org, ref); err != nil {
				problems["depot"] = err.Error()
			} else {
				depotCache[key], p.DepotID = id, id
			}
		}
		for field, msg := range problems {
			errs = append(errs, RowError{row.line, field, msg})
		}
		if first, dup := lineOf[p.ExternalOrderID]; dup && p.ExternalOrderID != "" {
			errs = append(errs, RowError{row.line, "external_order_id", fmt.Sprintf("duplicate of line %d", first)})
		}
		lineOf[p.ExternalOrderID] = row.line
		params = append(params, p)
	}

	ids := make([]string, len(params))
	for i, p := range params {
		ids[i] = p.ExternalOrderID
	}
	existing, err := store.New(h.pool).ExistingExternalIDs(r.Context(), store.ExistingExternalIDsParams{OrganizationID: org, ExternalOrderIds: ids})
	if err != nil {
		return err
	}
	exists := map[string]bool{}
	for _, id := range existing {
		exists[id] = true
	}
	toInsert := params[:0:0]
	for _, p := range params {
		switch {
		case exists[p.ExternalOrderID] && skipExisting:
		case exists[p.ExternalOrderID]:
			errs = append(errs, RowError{lineOf[p.ExternalOrderID], "external_order_id", "already imported (use ?skip_existing=true)"})
		default:
			toInsert = append(toInsert, p)
		}
	}

	if len(errs) > 0 {
		sort.SliceStable(errs, func(i, j int) bool { return errs[i].Row < errs[j].Row })
		total := len(errs)
		if total > maxReportErrors {
			errs = errs[:maxReportErrors]
		}
		return httpx.Unprocessable("IMPORT_REJECTED",
			fmt.Sprintf("%d problem(s) found; nothing was imported", total),
			map[string]any{"errors": errs, "total_errors": total, "rows": len(rows)})
	}

	err = database.WithTx(r.Context(), h.pool, func(tx pgx.Tx) error {
		var batchErr error
		store.New(tx).InsertOrderBatch(r.Context(), toInsert).Exec(func(i int, err error) {
			if err != nil && batchErr == nil {
				batchErr = err
			}
		})
		return batchErr
	})
	if database.IsUniqueViolation(err, "") {
		return httpx.Conflict("CONCURRENT_IMPORT", "some orders were imported concurrently; retry with ?skip_existing=true")
	} else if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{
		"imported": len(toInsert), "skipped_existing": len(params) - len(toInsert), "rows": len(rows),
	})
	return nil
}

// csvBody accepts a raw text/csv body or a multipart form with a "file" field (max 5 MB).
func csvBody(w http.ResponseWriter, r *http.Request) (io.Reader, error) {
	r.Body = http.MaxBytesReader(w, r.Body, maxImportBytes)
	mediaType, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type"))
	switch mediaType {
	case "text/csv", "application/csv", "text/plain":
		return r.Body, nil
	case "multipart/form-data":
		f, _, err := r.FormFile("file")
		if err != nil {
			return nil, httpx.BadRequest("INVALID_UPLOAD", "multipart upload needs a 'file' field (max 5 MB)", nil)
		}
		return f, nil
	}
	return nil, &httpx.Error{Status: http.StatusUnsupportedMediaType, Code: "UNSUPPORTED_MEDIA_TYPE",
		Message: "send text/csv or multipart/form-data with a 'file' field"}
}

func or(p *string, fallback string) string {
	if p != nil {
		return *p
	}
	return fallback
}

func orPtr[T any](p *T, fallback T) *T {
	if p != nil {
		return p
	}
	return &fallback
}
