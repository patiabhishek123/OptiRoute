package osrm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/osrm"
)

// fakeOSRM answers /route with the requested waypoints as the geometry and counts requests.
func fakeOSRM(t *testing.T, requests *int) *httptest.Server {
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		*requests++
		path := strings.TrimPrefix(r.URL.Path, "/route/v1/driving/")
		var coords []osrm.Point
		for _, c := range strings.Split(path, ";") {
			var p osrm.Point
			if _, err := fmt.Sscanf(c, "%f,%f", &p[0], &p[1]); err != nil {
				t.Errorf("bad coordinate %q", c)
			}
			coords = append(coords, p)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"code": "Ok", "routes": []any{map[string]any{"geometry": map[string]any{"coordinates": coords}}}})
	}))
}

func TestRouteStitchesChunksWithoutGapsOrDuplicates(t *testing.T) {
	requests := 0
	srv := fakeOSRM(t, &requests)
	defer srv.Close()
	c := osrm.New(srv.URL)
	c.MaxWaypoints = 3

	var waypoints []osrm.Point
	for i := range 6 {
		waypoints = append(waypoints, osrm.Point{85 + float64(i)/10, 20})
	}
	line, err := c.Route(context.Background(), waypoints)
	if err != nil {
		t.Fatal(err)
	}
	if requests != 3 { // [0,1,2] [2,3,4] [4,5]
		t.Errorf("requests = %d, want 3", requests)
	}
	if len(line) != len(waypoints) {
		t.Fatalf("line has %d points, want %d: %v", len(line), len(waypoints), line)
	}
	for i := range line {
		if line[i] != waypoints[i] {
			t.Errorf("point %d = %v, want %v", i, line[i], waypoints[i])
		}
	}
}

func TestRouteReportsOSRMErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"code":"NoRoute","message":"Impossible route between points"}`))
	}))
	defer srv.Close()
	_, err := osrm.New(srv.URL).Route(context.Background(), []osrm.Point{{85, 20}, {86, 21}})
	if err == nil || !strings.Contains(err.Error(), "NoRoute") {
		t.Fatalf("err = %v", err)
	}
}
