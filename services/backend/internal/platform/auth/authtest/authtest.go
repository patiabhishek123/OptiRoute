// Package authtest provides a token verifier for module tests: the token is a key into a map of
// principals, so tests exercise the real middleware and handlers without the identity module.
package authtest

import (
	"errors"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
)

type Verifier map[string]auth.Principal

func (v Verifier) Verify(token string) (auth.Principal, error) {
	if p, ok := v[token]; ok {
		return p, nil
	}
	return auth.Principal{}, errors.New("unknown test token")
}
