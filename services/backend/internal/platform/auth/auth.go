// Package auth is the module-neutral side of authentication: who is calling (Principal), the
// middleware that establishes it from a bearer token, and role checks. Modules depend on this
// package only; token issuing lives in the identity module.
package auth

import (
	"context"
	"net/http"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

type Role string

const (
	RoleAdmin      Role = "ADMIN"
	RoleDispatcher Role = "DISPATCHER"
	RoleViewer     Role = "VIEWER"
)

// Principal is the authenticated caller. Every business query is scoped by OrgID.
type Principal struct {
	UserID uuid.UUID
	OrgID  uuid.UUID
	Role   Role
}

// Verifier turns a bearer token into a Principal (implemented by identity's token service).
type Verifier interface {
	Verify(token string) (Principal, error)
}

type ctxKey struct{}

func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the caller; ok is false on unauthenticated routes.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// MustPrincipal is for handlers mounted behind Authenticate.
func MustPrincipal(r *http.Request) Principal {
	p, ok := FromContext(r.Context())
	if !ok {
		panic("auth: handler mounted without Authenticate middleware")
	}
	return p
}

// Authenticate requires a valid "Authorization: Bearer <access token>" header.
func Authenticate(v Verifier, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		if !ok || token == "" {
			writeErr(w, httpx.Unauthorized("missing bearer token"))
			return
		}
		p, err := v.Verify(token)
		if err != nil {
			writeErr(w, httpx.Unauthorized("invalid or expired token"))
			return
		}
		next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), p)))
	})
}

// RequireRole rejects callers whose role is not in roles (403).
func RequireRole(next http.Handler, roles ...Role) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p, ok := FromContext(r.Context())
		if !ok || !slices.Contains(roles, p.Role) {
			writeErr(w, httpx.Forbidden("insufficient role"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// Writers is the set of roles allowed to change operational data.
var Writers = []Role{RoleAdmin, RoleDispatcher}

func writeErr(w http.ResponseWriter, e *httpx.Error) {
	httpx.WriteJSON(w, e.Status, map[string]*httpx.Error{"error": e})
}
