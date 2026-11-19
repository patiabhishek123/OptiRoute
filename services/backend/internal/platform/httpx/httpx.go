// Package httpx holds the HTTP conventions shared by all modules (spec §12): JSON bodies,
// one error envelope {"error": {"code", "message", "details"}}, and common middleware.
package httpx

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"

	"github.com/google/uuid"
)

// Error is a client-facing error. Handlers return it; WriteError renders it.
type Error struct {
	Status  int    `json:"-"`
	Code    string `json:"code"`
	Message string `json:"message"`
	Details any    `json:"details,omitempty"`
}

func (e *Error) Error() string { return e.Code + ": " + e.Message }

func BadRequest(code, msg string, details any) *Error {
	return &Error{http.StatusBadRequest, code, msg, details}
}
func Unauthorized(msg string) *Error {
	return &Error{http.StatusUnauthorized, "UNAUTHORIZED", msg, nil}
}
func Forbidden(msg string) *Error { return &Error{http.StatusForbidden, "FORBIDDEN", msg, nil} }
func NotFound(what string) *Error {
	return &Error{http.StatusNotFound, "NOT_FOUND", what + " not found", nil}
}
func Conflict(code, msg string) *Error { return &Error{http.StatusConflict, code, msg, nil} }
func Unprocessable(code, msg string, details any) *Error {
	return &Error{http.StatusUnprocessableEntity, code, msg, details}
}

// HandlerFunc is an http.HandlerFunc that may return an error.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Handle adapts a HandlerFunc: *Error values are rendered as-is, anything else is logged and
// rendered as a generic 500 so internals never leak to clients.
func Handle(log *slog.Logger, h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			var e *Error
			if !errors.As(err, &e) {
				log.ErrorContext(r.Context(), "request failed", "err", err, "path", r.URL.Path)
				e = &Error{http.StatusInternalServerError, "INTERNAL", "internal error", nil}
			}
			WriteJSON(w, e.Status, map[string]*Error{"error": e})
		}
	})
}

func WriteJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

// DecodeJSON reads a size-limited JSON body, rejecting unknown fields and trailing data.
func DecodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return BadRequest("INVALID_JSON", fmt.Sprintf("invalid request body: %v", err), nil)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return BadRequest("INVALID_JSON", "request body must be a single JSON object", nil)
	}
	return nil
}

// Page reads ?limit= (default 100, max 500) and ?offset= (default 0) for list endpoints.
func Page(r *http.Request) (limit, offset int32, err error) {
	limit, offset = 100, 0
	parse := func(key string, dst *int32, lo, hi int64) {
		v := r.URL.Query().Get(key)
		if v == "" || err != nil {
			return
		}
		n, perr := strconv.ParseInt(v, 10, 32)
		if perr != nil || n < lo || n > hi {
			err = BadRequest("INVALID_PAGINATION", fmt.Sprintf("%s must be an integer in [%d, %d]", key, lo, hi), nil)
			return
		}
		*dst = int32(n)
	}
	parse("limit", &limit, 1, 500)
	parse("offset", &offset, 0, 1<<30)
	return limit, offset, err
}

// PathUUID parses a {name} path parameter as a UUID (404 when malformed, like a missing row).
func PathUUID(r *http.Request, name, what string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, NotFound(what)
	}
	return id, nil
}
