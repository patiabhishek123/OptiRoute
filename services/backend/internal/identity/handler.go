package identity

import (
	"log/slog"
	"net/http"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

type Handler struct {
	svc         *Service
	log         *slog.Logger
	allowSignup bool
}

func NewHandler(svc *Service, log *slog.Logger, allowSignup bool) *Handler {
	return &Handler{svc: svc, log: log, allowSignup: allowSignup}
}

// Routes mounts the identity API (spec §12.1). authn wraps handlers that need a logged-in caller.
func (h *Handler) Routes(mux *http.ServeMux, authn func(http.Handler) http.Handler) {
	handle := func(f httpx.HandlerFunc) http.Handler { return httpx.Handle(h.log, f) }

	mux.Handle("POST /api/v1/organizations", handle(h.signup))
	mux.Handle("POST /api/v1/auth/login", handle(h.login))
	mux.Handle("POST /api/v1/auth/refresh", handle(h.refresh))
	mux.Handle("POST /api/v1/auth/logout", handle(h.logout))

	mux.Handle("GET /api/v1/organizations/me", authn(handle(h.me)))
	mux.Handle("GET /api/v1/organizations/users",
		authn(auth.RequireRole(handle(h.listUsers), auth.RoleAdmin)))
	mux.Handle("POST /api/v1/organizations/users",
		authn(auth.RequireRole(handle(h.createUser), auth.RoleAdmin)))
}

func (h *Handler) signup(w http.ResponseWriter, r *http.Request) error {
	if !h.allowSignup {
		return httpx.Forbidden("self-service signup is disabled")
	}
	var in SignupInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	org, tokens, err := h.svc.Signup(r.Context(), in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, map[string]any{"organization": org, "tokens": tokens})
	return nil
}

func (h *Handler) login(w http.ResponseWriter, r *http.Request) error {
	var in struct {
		Organization string `json:"organization"` // slug
		Email        string `json:"email"`
		Password     string `json:"password"`
	}
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	tokens, err := h.svc.Login(r.Context(), in.Organization, in.Email, in.Password)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, tokens)
	return nil
}

type refreshBody struct {
	RefreshToken string `json:"refresh_token"`
}

func (h *Handler) refresh(w http.ResponseWriter, r *http.Request) error {
	var in refreshBody
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	tokens, err := h.svc.Refresh(r.Context(), in.RefreshToken)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, tokens)
	return nil
}

func (h *Handler) logout(w http.ResponseWriter, r *http.Request) error {
	var in refreshBody
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	if err := h.svc.Logout(r.Context(), in.RefreshToken); err != nil {
		return err
	}
	w.WriteHeader(http.StatusNoContent)
	return nil
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	p := auth.MustPrincipal(r)
	org, err := h.svc.Organization(r.Context(), p.OrgID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"organization": org,
		"user":         map[string]any{"id": p.UserID, "role": p.Role},
	})
	return nil
}

func (h *Handler) listUsers(w http.ResponseWriter, r *http.Request) error {
	users, err := h.svc.ListUsers(r.Context(), auth.MustPrincipal(r).OrgID)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"items": users})
	return nil
}

func (h *Handler) createUser(w http.ResponseWriter, r *http.Request) error {
	var in CreateUserInput
	if err := httpx.DecodeJSON(w, r, &in); err != nil {
		return err
	}
	u, err := h.svc.CreateUser(r.Context(), auth.MustPrincipal(r).OrgID, in)
	if err != nil {
		return err
	}
	httpx.WriteJSON(w, http.StatusCreated, u)
	return nil
}
