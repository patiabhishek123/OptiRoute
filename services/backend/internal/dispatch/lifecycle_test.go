package dispatch_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/dispatch"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth/authtest"
)

type lifecycle struct {
	*fixture
	url string
}

func newLifecycle(t *testing.T) lifecycle {
	f := setup(t)
	var user uuid.UUID
	if err := f.pool.QueryRow(f.ctx, `INSERT INTO core.users (organization_id, email, password_hash, role)
		VALUES ($1, 'dispatch@a.test', 'x', 'DISPATCHER') RETURNING id`, f.org).Scan(&user); err != nil {
		t.Fatal(err)
	}
	v := authtest.Verifier{
		"a":      {UserID: user, OrgID: f.org, Role: auth.RoleDispatcher},
		"viewer": {UserID: uuid.New(), OrgID: f.org, Role: auth.RoleViewer},
	}
	mux := http.NewServeMux()
	dispatch.NewHandler(f.pool, slog.New(slog.NewTextHandler(io.Discard, nil))).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(v, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return lifecycle{f, srv.URL}
}

func (l lifecycle) post(path, token string, body, out any) (int, string) {
	l.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest("POST", l.url+path, &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		l.t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if out != nil {
		_ = json.Unmarshal(raw, out)
	}
	var e struct{ Error struct{ Code string } }
	_ = json.Unmarshal(raw, &e)
	return resp.StatusCode, e.Error.Code
}

func (l lifecycle) plan(unassigned bool) uuid.UUID {
	l.t.Helper()
	id, _, err := dispatch.Materialize(l.ctx, l.pool, l.insertJob("COMPLETED", l.resultFor(unassigned)))
	if err != nil {
		l.t.Fatal(err)
	}
	return id
}

func (l lifecycle) status(plan uuid.UUID) string {
	var s string
	_ = l.pool.QueryRow(l.ctx, "SELECT status FROM routing.route_plans WHERE id = $1", plan).Scan(&s)
	return s
}

func path(plan uuid.UUID, action string) string {
	return "/api/v1/route-plans/" + plan.String() + "/" + action
}

func TestApproveRejectStateMachine(t *testing.T) {
	l := newLifecycle(t)
	p := l.plan(false)

	if code, _ := l.post(path(p, "approve"), "viewer", nil, nil); code != 403 {
		t.Errorf("viewer approve = %d, want 403", code)
	}
	if code, ec := l.post(path(p, "activate"), "a", nil, nil); code != 409 || ec != "INVALID_PLAN_STATE" {
		t.Errorf("activate DRAFT = %d %s, want 409", code, ec)
	}
	var approved dispatch.Plan
	if code, _ := l.post(path(p, "approve"), "a", nil, &approved); code != 200 || approved.Status != "APPROVED" ||
		approved.ApprovedBy == nil || approved.ApprovedAt == nil {
		t.Fatalf("approve = %d %+v", code, approved.PlanSummary)
	}
	if code, _ := l.post(path(p, "approve"), "a", nil, nil); code != 409 {
		t.Errorf("approve twice = %d, want 409", code)
	}

	r := l.plan(false)
	if code, _ := l.post(path(r, "reject"), "a", nil, nil); code != 200 || l.status(r) != "REJECTED" {
		t.Errorf("reject = %d %s", code, l.status(r))
	}
	if code, _ := l.post(path(uuid.New(), "approve"), "a", nil, nil); code != 404 {
		t.Errorf("unknown plan = %d, want 404", code)
	}
}

func TestActivateAndReplace(t *testing.T) {
	l := newLifecycle(t)
	first := l.plan(false) // serves O-1..O-3
	l.post(path(first, "approve"), "a", nil, nil)

	var act struct {
		Plan              dispatch.Plan `json:"plan"`
		SupersededPlanID  *uuid.UUID    `json:"superseded_plan_id"`
		UnplannedOrderIDs []uuid.UUID   `json:"unplanned_order_ids"`
	}
	if code, _ := l.post(path(first, "activate"), "a", nil, &act); code != 200 || act.Plan.Status != "ACTIVE" || act.SupersededPlanID != nil {
		t.Fatalf("activate = %d %+v", code, act)
	}
	for _, o := range l.orders {
		if s := l.orderStatus(o); s != "PLANNED" {
			t.Errorf("order %s after activation", s)
		}
	}
	if n := l.count("SELECT count(*) FROM routing.order_assignments WHERE route_plan_id = $1", first); n != 3 {
		t.Errorf("assignments = %d, want 3", n)
	}
	if n := l.count("SELECT count(*) FROM core.outbox WHERE topic = 'opti:plan-events' AND payload->>'plan_id' = $1", first.String()); n != 1 {
		t.Errorf("plan events = %d, want 1", n)
	}

	// A second plan that leaves O-3 unassigned: replacing needs explicit consent...
	second := l.plan(true)
	l.post(path(second, "approve"), "a", nil, nil)
	if code, ec := l.post(path(second, "activate"), "a", nil, nil); code != 409 || ec != "ACTIVE_PLAN_EXISTS" {
		t.Fatalf("activate over active = %d %s", code, ec)
	}
	// ...and then reports the order it un-plans.
	if code, _ := l.post(path(second, "activate"), "a", map[string]bool{"replace_active": true}, &act); code != 200 {
		t.Fatalf("replace = %d", code)
	}
	if act.SupersededPlanID == nil || *act.SupersededPlanID != first || len(act.UnplannedOrderIDs) != 1 || act.UnplannedOrderIDs[0] != l.orders[2] {
		t.Errorf("replacement report %+v", act)
	}
	if l.status(first) != "SUPERSEDED" || l.status(second) != "ACTIVE" {
		t.Errorf("statuses: first %s, second %s", l.status(first), l.status(second))
	}
	if s := l.orderStatus(l.orders[2]); s != "PENDING" {
		t.Errorf("un-planned order is %s, want PENDING", s)
	}
	if n := l.count("SELECT count(*) FROM routing.order_assignments"); n != 2 {
		t.Errorf("assignments after replace = %d, want 2", n)
	}
	if code, _ := l.post(path(second, "reject"), "a", nil, nil); code != 409 {
		t.Errorf("reject ACTIVE = %d, want 409", code)
	}
}

func TestActivationRefusesStaleOrStartedPlans(t *testing.T) {
	l := newLifecycle(t)
	stale := l.plan(false)
	l.post(path(stale, "approve"), "a", nil, nil)
	if _, err := l.pool.Exec(l.ctx, "UPDATE operations.orders SET status = 'CANCELLED' WHERE id = $1", l.orders[1]); err != nil {
		t.Fatal(err)
	}
	var resp struct {
		Error struct {
			Code    string
			Details []map[string]string
		} `json:"error"`
	}
	if code, _ := l.post(path(stale, "activate"), "a", nil, &resp); code != 409 || resp.Error.Code != "STALE_PLAN" ||
		len(resp.Error.Details) != 1 || resp.Error.Details[0]["external_order_id"] != "O-2" {
		t.Fatalf("stale activation = %d %+v", code, resp)
	}
	if l.status(stale) != "APPROVED" || l.orderStatus(l.orders[0]) != "PENDING" {
		t.Errorf("failed activation changed state: plan %s, order %s", l.status(stale), l.orderStatus(l.orders[0]))
	}

	// Restore, activate, then start a route: the plan can no longer be silently replaced.
	_, _ = l.pool.Exec(l.ctx, "UPDATE operations.orders SET status = 'PENDING' WHERE id = $1", l.orders[1])
	l.post(path(stale, "activate"), "a", nil, nil)
	_, _ = l.pool.Exec(l.ctx, "UPDATE routing.routes SET status = 'IN_PROGRESS' WHERE route_plan_id = $1", stale)
	next := l.plan(false)
	l.post(path(next, "approve"), "a", nil, nil)
	if code, ec := l.post(path(next, "activate"), "a", map[string]bool{"replace_active": true}, nil); code != 409 || ec != "ACTIVE_PLAN_IN_PROGRESS" {
		t.Errorf("replace started plan = %d %s", code, ec)
	}
}
