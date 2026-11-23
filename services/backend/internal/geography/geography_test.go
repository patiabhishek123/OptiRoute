package geography_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/geography"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
)

// csvBody is sent as-is with Content-Type text/csv.
type csvBody string

func setup(t *testing.T) (call func(method, path, token string, body, out any) int) {
	pool, _ := testdb.New(t)
	orgA, orgB := testdb.CreateOrg(t, pool, "org-a"), testdb.CreateOrg(t, pool, "org-b")
	v := authtest.Verifier{
		"admin-a":  {UserID: uuid.New(), OrgID: orgA, Role: auth.RoleAdmin},
		"viewer-a": {UserID: uuid.New(), OrgID: orgA, Role: auth.RoleViewer},
		"admin-b":  {UserID: uuid.New(), OrgID: orgB, Role: auth.RoleAdmin},
	}
	mux := http.NewServeMux()
	geography.NewHandler(geography.NewService(pool), slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return func(method, path, token string, body, out any) int {
		t.Helper()
		var buf bytes.Buffer
		ctype := "application/json"
		if csv, ok := body.(csvBody); ok {
			buf.WriteString(string(csv))
			ctype = "text/csv"
		} else if body != nil {
			_ = json.NewEncoder(&buf).Encode(body)
		}
		req, _ := http.NewRequest(method, srv.URL+path, &buf)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", ctype)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if out != nil {
			_ = json.NewDecoder(resp.Body).Decode(out)
		}
		return resp.StatusCode
	}
}

func TestDepotLifecycle(t *testing.T) {
	call := setup(t)
	var d geography.Depot
	if code := call("POST", "/api/v1/depots", "admin-a",
		map[string]any{"name": " Bhubaneswar Hub ", "lat": 20.2961, "lon": 85.8245}, &d); code != 201 {
		t.Fatalf("create = %d", code)
	}
	if d.Name != "Bhubaneswar Hub" || d.Lat != 20.2961 || d.Lon != 85.8245 || !d.IsActive {
		t.Fatalf("created %+v", d)
	}

	var got geography.Depot
	if code := call("GET", "/api/v1/depots/"+d.ID.String(), "viewer-a", nil, &got); code != 200 || got != d {
		t.Fatalf("get = %d %+v", code, got)
	}

	var upd geography.Depot
	if code := call("PATCH", "/api/v1/depots/"+d.ID.String(), "admin-a",
		map[string]any{"is_active": false, "lat": 20.30, "lon": 85.83}, &upd); code != 200 {
		t.Fatalf("patch = %d", code)
	}
	if upd.IsActive || upd.Lat != 20.30 || upd.Name != d.Name {
		t.Fatalf("patched %+v", upd)
	}

	var list struct {
		Items []geography.Depot
		Limit int
	}
	call("POST", "/api/v1/depots", "admin-a", map[string]any{"name": "Cuttack", "lat": 20.46, "lon": 85.88}, nil)
	call("GET", "/api/v1/depots?active=true", "admin-a", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "Cuttack" || list.Limit != 100 {
		t.Fatalf("active filter: %+v", list)
	}
	call("GET", "/api/v1/depots?limit=1&offset=1", "admin-a", nil, &list)
	if len(list.Items) != 1 || list.Items[0].Name != "Cuttack" {
		t.Fatalf("pagination: %+v", list.Items)
	}
}

func TestDepotValidationAuthAndTenancy(t *testing.T) {
	call := setup(t)
	var d geography.Depot
	call("POST", "/api/v1/depots", "admin-a", map[string]any{"name": "A", "lat": 20, "lon": 85}, &d)

	cases := []struct {
		name, method, path, token string
		body                      any
		want                      int
	}{
		{"viewer cannot create", "POST", "/api/v1/depots", "viewer-a", map[string]any{"name": "X", "lat": 1, "lon": 1}, 403},
		{"missing token", "GET", "/api/v1/depots", "nope", nil, 401},
		{"lat out of range", "POST", "/api/v1/depots", "admin-a", map[string]any{"name": "X", "lat": 91, "lon": 1}, 422},
		{"lat without lon", "PATCH", "/api/v1/depots/" + d.ID.String(), "admin-a", map[string]any{"lat": 1}, 422},
		{"other org cannot read", "GET", "/api/v1/depots/" + d.ID.String(), "admin-b", nil, 404},
		{"other org cannot update", "PATCH", "/api/v1/depots/" + d.ID.String(), "admin-b", map[string]any{"name": "pwned"}, 404},
		{"malformed id", "GET", "/api/v1/depots/not-a-uuid", "admin-a", nil, 404},
		{"bad pagination", "GET", "/api/v1/depots?limit=0", "admin-a", nil, 400},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := call(c.method, c.path, c.token, c.body, nil); got != c.want {
				t.Errorf("got %d, want %d", got, c.want)
			}
		})
	}

	var list struct{ Items []geography.Depot }
	call("GET", "/api/v1/depots", "admin-b", nil, &list)
	if len(list.Items) != 0 {
		t.Errorf("org B sees org A's depots: %+v", list.Items)
	}
}

func TestDepotCSVImport(t *testing.T) {
	call := setup(t)
	good := csvBody("name,latitude,longitude\nBhubaneswar Hub,20.2961,85.8245\nCuttack Hub,20.4625,85.8830\n")
	var res map[string]int
	if code := call("POST", "/api/v1/depots/bulk-import", "admin-a", good, &res); code != 201 || res["imported"] != 2 {
		t.Fatalf("import = %d %+v", code, res)
	}
	var list struct{ Items []geography.Depot }
	if call("GET", "/api/v1/depots", "admin-a", nil, &list); len(list.Items) != 2 {
		t.Fatalf("depots: %+v", list.Items)
	}
	// Names already used are rejected (orders and vehicles refer to depots by name)...
	var rej struct {
		Error struct {
			Code    string
			Details struct {
				Errors []struct {
					Row   int
					Field string
				}
			}
		}
	}
	bad := csvBody("name,lat,lon\ncuttack hub,20.46,85.88\nPuri Hub,95,85.83\nPuri Hub,19.8,85.83\nKonark,,86.09\n")
	if code := call("POST", "/api/v1/depots/bulk-import", "admin-a", bad, &rej); code != 422 || rej.Error.Code != "IMPORT_REJECTED" {
		t.Fatalf("bad import = %d %+v", code, rej)
	}
	got := map[string]bool{}
	for _, e := range rej.Error.Details.Errors {
		got[fmt.Sprintf("%d:%s", e.Row, e.Field)] = true
	}
	for _, want := range []string{"2:name", "3:lat", "4:name", "5:location"} {
		if !got[want] {
			t.Errorf("missing %s in %+v", want, rej.Error.Details.Errors)
		}
	}
	// ...unless skip_existing; other organizations are unaffected; viewers cannot import.
	if code := call("POST", "/api/v1/depots/bulk-import?skip_existing=true", "admin-a", good, &res); code != 201 || res["imported"] != 0 || res["skipped_existing"] != 2 {
		t.Errorf("skip_existing = %d %+v", code, res)
	}
	if code := call("POST", "/api/v1/depots/bulk-import", "admin-b", good, &res); code != 201 || res["imported"] != 2 {
		t.Errorf("org b = %d %+v", code, res)
	}
	if code := call("POST", "/api/v1/depots/bulk-import", "viewer-a", good, nil); code != 403 {
		t.Errorf("viewer = %d, want 403", code)
	}
}
