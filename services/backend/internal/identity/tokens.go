package identity

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
)

const issuer = "optiroute"

// Tokens issues and verifies short-lived HS256 access tokens. It implements auth.Verifier.
type Tokens struct {
	secret    []byte
	accessTTL time.Duration
	now       func() time.Time
}

func NewTokens(secret []byte, accessTTL time.Duration) (*Tokens, error) {
	if len(secret) < 32 {
		return nil, errors.New("JWT secret must be at least 32 bytes")
	}
	return &Tokens{secret: secret, accessTTL: accessTTL, now: time.Now}, nil
}

type claims struct {
	OrgID string `json:"org"`
	Role  string `json:"role"`
	jwt.RegisteredClaims
}

func (t *Tokens) Issue(p auth.Principal) (string, time.Time, error) {
	now := t.now()
	exp := now.Add(t.accessTTL)
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims{
		OrgID: p.OrgID.String(),
		Role:  string(p.Role),
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			Subject:   p.UserID.String(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(exp),
		},
	})
	signed, err := tok.SignedString(t.secret)
	return signed, exp, err
}

func (t *Tokens) Verify(token string) (auth.Principal, error) {
	var c claims
	_, err := jwt.ParseWithClaims(token, &c, func(*jwt.Token) (any, error) { return t.secret, nil },
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}), // no alg=none / RS confusion
		jwt.WithIssuer(issuer),
		jwt.WithExpirationRequired(),
		jwt.WithTimeFunc(t.now),
	)
	if err != nil {
		return auth.Principal{}, err
	}
	userID, err1 := uuid.Parse(c.Subject)
	orgID, err2 := uuid.Parse(c.OrgID)
	role := auth.Role(c.Role)
	if err := errors.Join(err1, err2); err != nil {
		return auth.Principal{}, fmt.Errorf("malformed claims: %w", err)
	}
	if role != auth.RoleAdmin && role != auth.RoleDispatcher && role != auth.RoleViewer {
		return auth.Principal{}, fmt.Errorf("unknown role %q", role)
	}
	return auth.Principal{UserID: userID, OrgID: orgID, Role: role}, nil
}

// newRefreshToken returns an opaque random token and the hash that is stored instead of it.
func newRefreshToken() (token string, hash []byte) {
	b := make([]byte, 32)
	_, _ = rand.Read(b)
	token = base64.RawURLEncoding.EncodeToString(b)
	return token, hashRefresh(token)
}

func hashRefresh(token string) []byte {
	h := sha256.Sum256([]byte(token))
	return h[:]
}
