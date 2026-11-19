package identity_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/identity"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/testdb"
)

var secret = []byte("test-secret-test-secret-test-secret!")

type client struct {
	t   *testing.T
	url string
}

// do sends JSON and decodes the JSON reply into out (if non-nil); returns the status code.
func (c client) do(method, path, token string, body, out any) int {
	c.t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req, _ := http.NewRequest(method, c.url+path, &buf)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		c.t.Fatal(err)
	}
	defer resp.Body.Close()
	if out != nil {
		_ = json.NewDecoder(resp.Body).Decode(out)
	}
	return resp.StatusCode
}

func newServer(t *testing.T) client {
	pool, _ := testdb.New(t)
	tokens, err := identity.NewTokens(secret, 15*time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	identity.NewHandler(identity.NewService(pool, tokens, time.Hour), log, true).
		Routes(mux, func(h http.Handler) http.Handler { return auth.Authenticate(tokens, h) })
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return client{t, srv.URL}
}

type signupResp struct {
	Organization identity.Organization `json:"organization"`
	Tokens       identity.TokenPair    `json:"tokens"`
}

func signup(c client, slug string) signupResp {
	c.t.Helper()
	var r signupResp
	code := c.do("POST", "/api/v1/organizations", "", map[string]string{
		"name": "Kalinga Couriers", "slug": slug,
		"admin_email": "Admin@Example.com", "admin_password": "correct horse battery",
	}, &r)
	if code != http.StatusCreated {
		c.t.Fatalf("signup = %d", code)
	}
	return r
}

func TestSignupLoginAndMe(t *testing.T) {
	c := newServer(t)
	s := signup(c, "kalinga")
	if s.Organization.Timezone != "Asia/Kolkata" || s.Organization.Currency != "INR" || s.Tokens.TokenType != "Bearer" {
		t.Fatalf("defaults not applied: %+v", s)
	}

	var me struct {
		Organization identity.Organization `json:"organization"`
		User         struct{ Role string } `json:"user"`
	}
	if code := c.do("GET", "/api/v1/organizations/me", s.Tokens.AccessToken, nil, &me); code != 200 {
		t.Fatalf("me = %d", code)
	}
	if me.Organization.Slug != "kalinga" || me.User.Role != "ADMIN" {
		t.Fatalf("me = %+v", me)
	}

	if code := c.do("POST", "/api/v1/organizations", "", map[string]string{
		"name": "X", "slug": "kalinga", "admin_email": "a@b.co", "admin_password": "0123456789",
	}, nil); code != http.StatusConflict {
		t.Errorf("duplicate slug = %d, want 409", code)
	}

	login := func(org, email, pw string) int {
		return c.do("POST", "/api/v1/auth/login", "", map[string]string{
			"organization": org, "email": email, "password": pw,
		}, nil)
	}
	if code := login("kalinga", "ADMIN@example.com ", "correct horse battery"); code != 200 {
		t.Errorf("login (case/space-insensitive email) = %d, want 200", code)
	}
	if code := login("kalinga", "admin@example.com", "wrong password!"); code != 401 {
		t.Errorf("wrong password = %d, want 401", code)
	}
	if code := login("nope", "admin@example.com", "correct horse battery"); code != 401 {
		t.Errorf("unknown org = %d, want 401", code)
	}
}

func TestSignupValidation(t *testing.T) {
	c := newServer(t)
	var body struct {
		Error struct {
			Code    string            `json:"code"`
			Details map[string]string `json:"details"`
		} `json:"error"`
	}
	code := c.do("POST", "/api/v1/organizations", "", map[string]string{
		"name": "", "slug": "Bad Slug", "timezone": "Mars/Base", "currency": "rupee",
		"admin_email": "not-an-email", "admin_password": "short",
	}, &body)
	if code != http.StatusUnprocessableEntity || body.Error.Code != "VALIDATION_FAILED" {
		t.Fatalf("got %d %+v", code, body)
	}
	for _, f := range []string{"name", "slug", "timezone", "currency", "email", "password"} {
		if body.Error.Details[f] == "" {
			t.Errorf("missing validation detail for %q", f)
		}
	}
	if code := c.do("POST", "/api/v1/organizations", "", map[string]any{"slug": "x", "extra": 1}, nil); code != 400 {
		t.Errorf("unknown JSON field = %d, want 400", code)
	}
}

func TestRefreshRotationAndReuseDetection(t *testing.T) {
	c := newServer(t)
	first := signup(c, "rotate").Tokens

	var second identity.TokenPair
	if code := c.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": first.RefreshToken}, &second); code != 200 {
		t.Fatalf("refresh = %d", code)
	}
	if second.RefreshToken == first.RefreshToken {
		t.Fatal("refresh token was not rotated")
	}
	// Replaying the rotated token looks like theft: it fails and kills the whole family...
	if code := c.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": first.RefreshToken}, nil); code != 401 {
		t.Fatalf("reused refresh = %d, want 401", code)
	}
	// ...including the legitimate newer token.
	if code := c.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": second.RefreshToken}, nil); code != 401 {
		t.Fatalf("family not revoked after reuse: %d", code)
	}
}

func TestLogoutRevokesRefresh(t *testing.T) {
	c := newServer(t)
	tok := signup(c, "logout").Tokens
	if code := c.do("POST", "/api/v1/auth/logout", "", map[string]string{"refresh_token": tok.RefreshToken}, nil); code != 204 {
		t.Fatalf("logout = %d", code)
	}
	if code := c.do("POST", "/api/v1/auth/refresh", "", map[string]string{"refresh_token": tok.RefreshToken}, nil); code != 401 {
		t.Fatalf("refresh after logout = %d, want 401", code)
	}
	if code := c.do("POST", "/api/v1/auth/logout", "", map[string]string{"refresh_token": "unknown"}, nil); code != 204 {
		t.Fatalf("logout is not idempotent: %d", code)
	}
}

func TestRolesAndTenantScoping(t *testing.T) {
	c := newServer(t)
	admin := signup(c, "roles").Tokens.AccessToken
	other := signup(c, "other-org").Tokens.AccessToken

	newUser := map[string]string{"email": "dispatch@example.com", "password": "dispatcher-pass", "role": "dispatcher"}
	if code := c.do("POST", "/api/v1/organizations/users", admin, newUser, nil); code != 201 {
		t.Fatalf("create dispatcher = %d", code)
	}
	if code := c.do("POST", "/api/v1/organizations/users", admin, newUser, nil); code != 409 {
		t.Errorf("duplicate email = %d, want 409", code)
	}

	var tok identity.TokenPair
	c.do("POST", "/api/v1/auth/login", "", map[string]string{
		"organization": "roles", "email": "dispatch@example.com", "password": "dispatcher-pass",
	}, &tok)
	if code := c.do("GET", "/api/v1/organizations/users", tok.AccessToken, nil, nil); code != 403 {
		t.Errorf("dispatcher listing users = %d, want 403", code)
	}

	var list struct{ Items []identity.User }
	c.do("GET", "/api/v1/organizations/users", admin, nil, &list)
	if len(list.Items) != 2 {
		t.Errorf("admin sees %d users, want 2", len(list.Items))
	}
	c.do("GET", "/api/v1/organizations/users", other, nil, &list)
	if len(list.Items) != 1 || list.Items[0].Email != "admin@example.com" {
		t.Errorf("other org must only see its own admin, got %+v", list.Items)
	}

	if code := c.do("GET", "/api/v1/organizations/me", "", nil, nil); code != 401 {
		t.Errorf("no token = %d, want 401", code)
	}
	if code := c.do("GET", "/api/v1/organizations/me", admin+"x", nil, nil); code != 401 {
		t.Errorf("tampered token = %d, want 401", code)
	}
}

func TestTokenVerification(t *testing.T) {
	tokens, _ := identity.NewTokens(secret, time.Minute)
	p := auth.Principal{UserID: uuid.New(), OrgID: uuid.New(), Role: auth.RoleDispatcher}
	signed, _, err := tokens.Issue(p)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := tokens.Verify(signed); err != nil || got != p {
		t.Fatalf("round trip: %+v, %v", got, err)
	}

	short, _ := identity.NewTokens(secret, -time.Second)
	expired, _, _ := short.Issue(p)
	if _, err := tokens.Verify(expired); err == nil {
		t.Error("expired token accepted")
	}
	otherKey, _ := identity.NewTokens([]byte("another-secret-another-secret-123"), time.Minute)
	forged, _, _ := otherKey.Issue(p)
	if _, err := tokens.Verify(forged); err == nil {
		t.Error("token signed with another key accepted")
	}
	none, _ := jwt.NewWithClaims(jwt.SigningMethodNone, jwt.MapClaims{
		"sub": p.UserID.String(), "org": p.OrgID.String(), "role": "ADMIN", "iss": "optiroute",
		"exp": time.Now().Add(time.Hour).Unix(),
	}).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if _, err := tokens.Verify(none); err == nil {
		t.Error("alg=none token accepted")
	}
	if _, err := identity.NewTokens([]byte("short"), time.Minute); err == nil {
		t.Error("short secret accepted")
	}
}
