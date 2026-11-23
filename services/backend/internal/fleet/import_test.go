package fleet_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/fleet"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/csvx"
)

func (e env) upload(path, token, csv string, out any) int {
	e.t.Helper()
	req, _ := http.NewRequest("POST", e.url+path, strings.NewReader(csv))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "text/csv")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		e.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		b, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(b, out)
	}
	return resp.StatusCode
}

type rejection struct {
	Error struct {
		Code    string
		Details struct {
			Errors      []csvx.RowError
			TotalErrors int `json:"total_errors"`
		}
	} `json:"error"`
}

func (r rejection) has(t *testing.T, want ...string) {
	t.Helper()
	got := map[string]bool{}
	for _, e := range r.Error.Details.Errors {
		got[fmt.Sprintf("%d:%s", e.Row, e.Field)] = true
	}
	for _, w := range want {
		if !got[w] {
			t.Errorf("missing error %s in %+v", w, r.Error.Details.Errors)
		}
	}
	if len(r.Error.Details.Errors) != len(want) {
		t.Errorf("got %d errors, want %d: %+v", len(r.Error.Details.Errors), len(want), r.Error.Details.Errors)
	}
}

const vehiclesCSV = "registration_number,depot,vehicle_type,capacity_weight_kg,capacity_volume_m3,cost_per_km,fixed_cost\n" +
	"OD-02-AB-0001,hub,van,400,3,12,600\n" +
	"OD-02-AB-0002,HUB,truck,1200,8,20,\n"

func TestImportVehicles(t *testing.T) {
	e := setup(t)
	var res map[string]int
	if code := e.upload("/api/v1/vehicles/bulk-import", "a", vehiclesCSV, &res); code != 201 || res["imported"] != 2 {
		t.Fatalf("import = %d %+v", code, res)
	}
	var list struct{ Items []fleet.Vehicle }
	e.call("GET", "/api/v1/vehicles", "a", nil, &list)
	if len(list.Items) != 2 || list.Items[0].DepotID != e.depotA || list.Items[1].FixedCost != 0 {
		t.Fatalf("imported: %+v", list.Items)
	}

	// Same file again: rejected unless skip_existing, which makes the retry a no-op.
	var rej rejection
	if code := e.upload("/api/v1/vehicles/bulk-import", "a", vehiclesCSV, &rej); code != 422 {
		t.Errorf("re-upload = %d, want 422", code)
	}
	rej.has(t, "2:registration_number", "3:registration_number")
	if code := e.upload("/api/v1/vehicles/bulk-import?skip_existing=true", "a",
		vehiclesCSV+"OD-02-AB-0003,hub,bike,30,0.15,4,200\n", &res); code != 201 || res["imported"] != 1 || res["skipped_existing"] != 2 {
		t.Errorf("skip_existing = %d %+v", code, res)
	}

	// Another organization's depot name does not resolve to ours.
	if code := e.upload("/api/v1/vehicles/bulk-import", "b", vehiclesCSV, &res); code != 201 {
		t.Errorf("org b import = %d", code)
	}
	if code := e.upload("/api/v1/vehicles/bulk-import", "viewer-a", vehiclesCSV, nil); code != 403 {
		t.Errorf("viewer import = %d, want 403", code)
	}
}

func TestImportVehiclesIsAllOrNothing(t *testing.T) {
	e := setup(t)
	csv := "registration,depot,type,capacity_kg,capacity_m3,cost_per_km\n" +
		"OK-1,hub,van,400,3,12\n" + //        line 2: ok
		"OK-1,hub,van,400,3,12\n" + //        line 3: duplicate in file
		"BAD-3,Puri Hub,van,400,3,12\n" + //  line 4: unknown depot
		"BAD-4,hub,van,lots,3,12\n" + //      line 5: not a number
		"BAD-5,hub,van,400,-1,12\n" + //      line 6: negative
		"BAD-6,,van,400,3,12\n" //            line 7: missing depot
	var rej rejection
	if code := e.upload("/api/v1/vehicles/bulk-import", "a", csv, &rej); code != 422 || rej.Error.Code != "IMPORT_REJECTED" {
		t.Fatalf("import = %d %+v", code, rej)
	}
	rej.has(t, "3:registration_number", "4:depot", "5:capacity_weight_kg", "6:capacity_volume_m3", "7:depot")
	var list struct{ Items []fleet.Vehicle }
	if e.call("GET", "/api/v1/vehicles", "a", nil, &list); len(list.Items) != 0 {
		t.Errorf("rows written despite rejection: %+v", list.Items)
	}

	if code := e.upload("/api/v1/vehicles/bulk-import", "a", "registration,depot\nX,hub\n", &rej); code != 400 {
		t.Errorf("missing columns = %d, want 400", code)
	}
}

func TestImportDrivers(t *testing.T) {
	e := setup(t)
	csv := "name,phone,shift_start,shift_end,break_minutes,max_overtime\n" +
		"Ramesh Behera,+91 9437000101,08:00,18:00,30,60\n" +
		"Sunita Das,,09:00,17:00,,\n"
	var res map[string]int
	if code := e.upload("/api/v1/drivers/bulk-import", "a", csv, &res); code != 201 || res["imported"] != 2 {
		t.Fatalf("import = %d %+v", code, res)
	}
	var list struct{ Items []fleet.Driver }
	e.call("GET", "/api/v1/drivers", "a", nil, &list)
	if len(list.Items) != 2 {
		t.Fatalf("imported: %+v", list.Items)
	}
	byName := map[string]fleet.Driver{}
	for _, d := range list.Items {
		byName[d.Name] = d
	}
	if d := byName["Sunita Das"]; d.ShiftStart != "09:00" || d.Phone != nil || d.BreakDurationMinutes != 0 {
		t.Errorf("Sunita: %+v", d)
	}
	if d := byName["Ramesh Behera"]; d.Phone == nil || d.MaxOvertimeMinutes != 60 {
		t.Errorf("Ramesh: %+v", d)
	}

	bad := "name,shift_start,shift_end,break_minutes\n" +
		"ramesh behera,08:00,18:00,30\n" + // line 2: exists (case-insensitive)
		"New One,8am,18:00,30\n" + //        line 3: bad clock
		"Late Shift,18:00,08:00,30\n" + //   line 4: overnight
		"Long Break,08:00,18:00,900\n" + //  line 5: break out of range
		"Long Break,08:00,18:00,30\n" //     line 6: duplicate in file
	var rej rejection
	if code := e.upload("/api/v1/drivers/bulk-import", "a", bad, &rej); code != 422 {
		t.Fatalf("bad import = %d", code)
	}
	rej.has(t, "2:name", "3:shift_start", "4:shift_end", "5:break_duration_minutes", "6:name")
}
