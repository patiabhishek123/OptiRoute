package orders_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/geography"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/orders"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
)

type env struct {
	t     *testing.T
	url   string
	pool  *pgxpool.Pool
	depot uuid.UUID
}

func setup(t *testing.T) env {
	pool, _ := testdb.New(t)
	orgA, orgB := testdb.CreateOrg(t, pool, "org-a"), testdb.CreateOrg(t, pool, "org-b")
	var depot uuid.UUID
	if err := pool.QueryRow(context.Background(), `INSERT INTO core.depots (organization_id, name, location)
		VALUES ($1, 'Bhubaneswar Hub', 'SRID=4326;POINT(85.82 20.29)') RETURNING id`, orgA).Scan(&depot); err != nil {
		t.Fatal(err)
	}
	v := authtest.Verifier{
		"a": {UserID: uuid.New(), OrgID: orgA, Role: auth.RoleDispatcher},
		"b": {UserID: uuid.New(), OrgID: orgB, Role: auth.RoleDispatcher},
	}
	mux := http.NewServeMux()
	orders.NewHandler(pool, geography.NewService(pool), slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return env{t, srv.URL, pool, depot}
}

func (e env) send(method, path, token, contentType string, body io.Reader, out any) int {
	e.t.Helper()
	req, _ := http.NewRequest(method, e.url+path, body)
	req.Header.Set("Authorization", "Bearer "+token)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func (e env) json(method, path, token string, body, out any) int {
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	return e.send(method, path, token, "application/json", &buf, out)
}

func (e env) count() int {
	var n int
	_ = e.pool.QueryRow(context.Background(), "SELECT count(*) FROM operations.orders").Scan(&n)
	return n
}

var order = map[string]any{
	"external_order_id": "ORD-1", "depot": "bhubaneswar hub", "planning_date": "2026-11-02",
	"customer_name": "Ramesh Sahoo", "lat": 20.30, "lon": 85.84, "demand_weight_kg": 12.5,
	"time_window_start": "09:00", "time_window_end": "13:00",
}

func TestCreateOrderUsesOrgTimezoneAndDepotName(t *testing.T) {
	e := setup(t)
	var o orders.Order
	if code := e.json("POST", "/api/v1/orders", "a", order, &o); code != 201 {
		t.Fatalf("create = %d", code)
	}
	if o.DepotID == nil || *o.DepotID != e.depot || o.Status != "PENDING" || o.ServiceDurationMinutes != 5 || o.Priority != 1 {
		t.Fatalf("order %+v", o)
	}
	// 09:00 in Asia/Kolkata (UTC+05:30) is 03:30 UTC.
	if want := time.Date(2026, 11, 2, 3, 30, 0, 0, time.UTC); !o.TimeWindowStart.Equal(want) {
		t.Errorf("window start %v, want %v", o.TimeWindowStart.UTC(), want)
	}
	if code := e.json("POST", "/api/v1/orders", "a", order, nil); code != 409 {
		t.Errorf("duplicate external id = %d, want 409", code)
	}
	bad := map[string]any{"external_order_id": "X", "planning_date": "2026-11-02", "customer_name": "C",
		"lat": 20, "lon": 85, "time_window_start": "13:00", "time_window_end": "09:00", "depot": "nowhere"}
	var resp struct {
		Error struct{ Details map[string]string } `json:"error"`
	}
	if code := e.json("POST", "/api/v1/orders", "a", bad, &resp); code != 422 ||
		resp.Error.Details["time_window_end"] == "" || resp.Error.Details["depot"] == "" {
		t.Errorf("invalid order = %d %+v", code, resp)
	}
	if code := e.json("GET", "/api/v1/orders/"+o.ID.String(), "b", nil, nil); code != 404 {
		t.Errorf("other org get = %d, want 404", code)
	}
}

const goodCSV = "\ufeffOrder_ID,Customer,Phone,Latitude,Longitude,Weight_kg,From,To,Depot,Priority\n" +
	"A-1,Sahoo Stores,+91 90000 00001,20.31,85.82,12,09:00,12:00,Bhubaneswar Hub,2\n" +
	"A-2,Das Pharmacy,,20.27,85.84,3.5,13:00,17:00,,\n" +
	"A-3,Mishra Traders,,20.35,85.80,40,2026-11-02T10:00:00+05:30,2026-11-02T14:00:00+05:30,,\n"

func TestCSVImport(t *testing.T) {
	e := setup(t)
	var res map[string]int
	if code := e.send("POST", "/api/v1/orders/bulk-import?planning_date=2026-11-02&batch_id=morning", "a",
		"text/csv", strings.NewReader(goodCSV), &res); code != 201 || res["imported"] != 3 {
		t.Fatalf("import = %d %+v", code, res)
	}
	var list struct{ Items []orders.Order }
	e.json("GET", "/api/v1/orders?planning_date=2026-11-02&batch_id=morning", "a", nil, &list)
	if len(list.Items) != 3 || list.Items[0].ExternalOrderID != "A-1" || list.Items[0].Priority != 2 ||
		list.Items[0].DepotID == nil || *list.Items[0].CustomerPhone != "+91 90000 00001" || list.Items[1].CustomerPhone != nil {
		t.Fatalf("imported rows: %+v", list.Items)
	}

	// Same file again: rejected unless skip_existing, which makes the retry a no-op.
	if code := e.send("POST", "/api/v1/orders/bulk-import?planning_date=2026-11-02", "a", "text/csv", strings.NewReader(goodCSV), nil); code != 422 {
		t.Errorf("re-upload = %d, want 422", code)
	}
	if code := e.send("POST", "/api/v1/orders/bulk-import?planning_date=2026-11-02&skip_existing=true", "a",
		"text/csv", strings.NewReader(goodCSV+"A-4,New Shop,,20.30,85.83,1,09:00,18:00,,\n"), &res); code != 201 ||
		res["imported"] != 1 || res["skipped_existing"] != 3 {
		t.Errorf("skip_existing = %d %+v", code, res)
	}

	// Multipart upload, planning_date as a column.
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "orders.csv")
	fmt.Fprint(fw, "id,name,lat,lon,window_start,window_end,date\nM-1,Patra,20.3,85.8,09:00,10:00,2026-11-03\n")
	mw.Close()
	if code := e.send("POST", "/api/v1/orders/bulk-import", "a", mw.FormDataContentType(), &body, &res); code != 201 || res["imported"] != 1 {
		t.Errorf("multipart = %d %+v", code, res)
	}
}

func TestCSVImportIsAllOrNothingWithRowErrors(t *testing.T) {
	e := setup(t)
	csv := "order_id,customer,lat,lon,from,to,depot,weight\n" +
		"B-1,Fine,20.3,85.8,09:00,12:00,,1\n" + //   line 2: ok
		"B-2,Bad lat,95,85.8,09:00,12:00,,1\n" + //   line 3: lat out of range
		"B-3,Backwards,20.3,85.8,15:00,12:00,,1\n" + // line 4: window end before start
		"B-1,Duplicate,20.3,85.8,09:00,12:00,,1\n" + // line 5: duplicate id in file
		"B-5,Ghost depot,20.3,85.8,09:00,12:00,Puri Hub,1\n" + // line 6: unknown depot
		"B-6,Heavy,20.3,85.8,09:00,12:00,,lots\n" // line 7: weight not a number
	var resp struct {
		Error struct {
			Code    string
			Details struct {
				Errors      []orders.RowError
				TotalErrors int `json:"total_errors"`
			}
		} `json:"error"`
	}
	if code := e.send("POST", "/api/v1/orders/bulk-import?planning_date=2026-11-02", "a", "text/csv",
		strings.NewReader(csv), &resp); code != 422 || resp.Error.Code != "IMPORT_REJECTED" {
		t.Fatalf("import = %d %+v", code, resp)
	}
	got := map[string]bool{}
	for _, re := range resp.Error.Details.Errors {
		got[fmt.Sprintf("%d:%s", re.Row, re.Field)] = true
	}
	for _, want := range []string{"3:lat", "4:time_window_end", "5:external_order_id", "6:depot", "7:demand_weight_kg"} {
		if !got[want] {
			t.Errorf("missing error %s in %+v", want, resp.Error.Details.Errors)
		}
	}
	if got["2:lat"] || e.count() != 0 {
		t.Errorf("valid line flagged or rows inserted (count=%d)", e.count())
	}

	for name, c := range map[string]struct {
		body, ct string
		want     int
	}{
		"missing columns": {"order_id,customer\nX,Y\n", "text/csv", 400},
		"no date":         {"order_id,customer,lat,lon,from,to\nX,Y,1,1,09:00,10:00\n", "text/csv", 400},
		"json body":       {"{}", "application/json", 415},
	} {
		if code := e.send("POST", "/api/v1/orders/bulk-import", "a", c.ct, strings.NewReader(c.body), nil); code != c.want {
			t.Errorf("%s = %d, want %d", name, code, c.want)
		}
	}
}

func TestOrderLifecycle(t *testing.T) {
	e := setup(t)
	var o orders.Order
	e.json("POST", "/api/v1/orders", "a", order, &o)
	path := "/api/v1/orders/" + o.ID.String()

	var p orders.Order
	if code := e.json("PATCH", path, "a", map[string]any{"time_window_end": "14:30", "priority": 3}, &p); code != 200 {
		t.Fatalf("patch = %d", code)
	}
	if p.Priority != 3 || !p.TimeWindowEnd.Equal(time.Date(2026, 11, 2, 9, 0, 0, 0, time.UTC)) || p.TimeWindowStart != o.TimeWindowStart {
		t.Errorf("patched %+v", p)
	}
	if code := e.json("PATCH", path, "a", map[string]any{"time_window_end": "08:00"}, nil); code != 422 {
		t.Errorf("window end before stored start = %d, want 422", code)
	}

	if code := e.json("POST", path+"/cancel", "a", nil, &p); code != 200 || p.Status != "CANCELLED" {
		t.Fatalf("cancel = %d %+v", code, p)
	}
	if code := e.json("PATCH", path, "a", map[string]any{"priority": 1}, nil); code != 409 {
		t.Errorf("edit cancelled = %d, want 409", code)
	}
	if code := e.json("POST", path+"/cancel", "a", nil, nil); code != 409 {
		t.Errorf("cancel twice = %d, want 409", code)
	}

	var d orders.Order
	e.json("POST", "/api/v1/orders", "a", map[string]any{
		"external_order_id": "ORD-2", "planning_date": "2026-11-02", "customer_name": "C",
		"lat": 20.3, "lon": 85.8, "time_window_start": "09:00", "time_window_end": "17:00",
	}, &d)
	_, _ = e.pool.Exec(context.Background(), "UPDATE operations.orders SET status='DISPATCHED' WHERE id=$1", d.ID)
	if code := e.json("POST", "/api/v1/orders/"+d.ID.String()+"/cancel", "a", nil, nil); code != 409 {
		t.Errorf("cancel dispatched = %d, want 409", code)
	}
	_, _ = e.pool.Exec(context.Background(), "UPDATE operations.orders SET status='PLANNED' WHERE id=$1", d.ID)
	if code := e.json("POST", "/api/v1/orders/"+d.ID.String()+"/cancel", "a", nil, nil); code != 409 {
		t.Errorf("cancel planned = %d, want 409 (it is a stop on the active plan)", code)
	}
	if code := e.json("POST", "/api/v1/orders/"+uuid.NewString()+"/cancel", "a", nil, nil); code != 404 {
		t.Errorf("cancel unknown = %d, want 404", code)
	}
}

func TestImportThousandRows(t *testing.T) {
	e := setup(t)
	var b strings.Builder
	b.WriteString("order_id,customer,lat,lon,from,to,weight\n")
	for i := range 1000 {
		fmt.Fprintf(&b, "BULK-%04d,Customer %d,%.5f,%.5f,09:00,18:00,%d\n", i, i, 20.2+float64(i%100)/1000, 85.8+float64(i/100)/1000, i%20)
	}
	start := time.Now()
	var res map[string]int
	if code := e.send("POST", "/api/v1/orders/bulk-import?planning_date=2026-11-02", "a", "text/csv",
		strings.NewReader(b.String()), &res); code != 201 || res["imported"] != 1000 {
		t.Fatalf("import = %d %+v", code, res)
	}
	t.Logf("imported 1000 orders in %v", time.Since(start))
	if e.count() != 1000 {
		t.Fatalf("count = %d", e.count())
	}
}
