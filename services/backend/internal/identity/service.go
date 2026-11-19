// Package identity owns organizations, users and authentication (spec §6): password login,
// short-lived JWT access tokens and rotating refresh tokens with reuse detection.
package identity

import (
	"context"
	"errors"
	"net/mail"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"golang.org/x/crypto/bcrypt"

	"github.com/patiabhishek123/OptiRoute/services/backend/internal/identity/store"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/auth"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/database"
	"github.com/patiabhishek123/OptiRoute/services/backend/internal/platform/httpx"
)

// DefaultBcryptCost is used in production; tests lower it with WithBcryptCost.
const DefaultBcryptCost = 12

var (
	errInvalidCredentials = httpx.Unauthorized("invalid organization, email or password")
	errInvalidRefresh     = httpx.Unauthorized("invalid or expired refresh token")
	slugPattern           = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)
	currencyPattern       = regexp.MustCompile(`^[A-Z]{3}$`)
)

type Service struct {
	pool       *pgxpool.Pool
	tokens     *Tokens
	refreshTTL time.Duration
	bcryptCost int
	dummyHash  []byte // compared against when the user does not exist (constant-ish timing)
}

func NewService(pool *pgxpool.Pool, tokens *Tokens, refreshTTL time.Duration) *Service {
	return (&Service{pool: pool, tokens: tokens, refreshTTL: refreshTTL}).WithBcryptCost(DefaultBcryptCost)
}

// WithBcryptCost sets the password hashing cost (tests use bcrypt.MinCost to stay fast).
func (s *Service) WithBcryptCost(cost int) *Service {
	s.bcryptCost = cost
	s.dummyHash, _ = bcrypt.GenerateFromPassword([]byte("not-a-real-password"), cost)
	return s
}

type TokenPair struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
	TokenType    string `json:"token_type"`
	ExpiresIn    int    `json:"expires_in"` // seconds until the access token expires
}

type Organization struct {
	ID        uuid.UUID `json:"id"`
	Name      string    `json:"name"`
	Slug      string    `json:"slug"`
	Timezone  string    `json:"timezone"`
	Currency  string    `json:"currency"`
	CreatedAt time.Time `json:"created_at"`
}

type User struct {
	ID             uuid.UUID `json:"id"`
	OrganizationID uuid.UUID `json:"organization_id"`
	Email          string    `json:"email"`
	Role           string    `json:"role"`
	CreatedAt      time.Time `json:"created_at"`
}

type SignupInput struct {
	Name     string `json:"name"`
	Slug     string `json:"slug"`
	Timezone string `json:"timezone"`
	Currency string `json:"currency"`
	Email    string `json:"admin_email"`
	Password string `json:"admin_password"`
}

// Signup creates an organization and its first ADMIN user, and logs that user in.
func (s *Service) Signup(ctx context.Context, in SignupInput) (Organization, TokenPair, error) {
	in.Name = strings.TrimSpace(in.Name)
	in.Slug = strings.ToLower(strings.TrimSpace(in.Slug))
	in.Timezone = defaultStr(in.Timezone, "Asia/Kolkata")
	in.Currency = strings.ToUpper(defaultStr(in.Currency, "INR"))
	problems := map[string]string{}
	if in.Name == "" {
		problems["name"] = "required"
	}
	if !slugPattern.MatchString(in.Slug) {
		problems["slug"] = "2-63 chars: lowercase letters, digits, hyphens"
	}
	if _, err := time.LoadLocation(in.Timezone); err != nil {
		problems["timezone"] = "unknown IANA timezone"
	}
	if !currencyPattern.MatchString(in.Currency) {
		problems["currency"] = "ISO 4217 code, e.g. INR"
	}
	email, hash, err := s.credentials(in.Email, in.Password, problems)
	if err != nil {
		return Organization{}, TokenPair{}, err
	}

	var org store.CoreOrganization
	var pair TokenPair
	err = database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		var err error
		org, err = q.CreateOrganization(ctx, store.CreateOrganizationParams{
			Name: in.Name, Slug: in.Slug, Timezone: in.Timezone, Currency: in.Currency,
		})
		if database.IsUniqueViolation(err, "organizations_slug_key") {
			return httpx.Conflict("SLUG_TAKEN", "organization slug is already in use")
		} else if err != nil {
			return err
		}
		u, err := q.CreateUser(ctx, store.CreateUserParams{
			OrganizationID: org.ID, Email: email, PasswordHash: string(hash), Role: string(auth.RoleAdmin),
		})
		if err != nil {
			return err
		}
		pair, err = s.issue(ctx, q, auth.Principal{UserID: u.ID, OrgID: org.ID, Role: auth.RoleAdmin}, uuid.New())
		return err
	})
	return toOrg(org), pair, err
}

func (s *Service) Login(ctx context.Context, slug, email, password string) (TokenPair, error) {
	q := store.New(s.pool)
	u, err := q.GetUserForLogin(ctx, store.GetUserForLoginParams{
		Slug: strings.ToLower(strings.TrimSpace(slug)), Email: normalizeEmail(email),
	})
	if errors.Is(err, pgx.ErrNoRows) {
		_ = bcrypt.CompareHashAndPassword(s.dummyHash, []byte(password)) // same cost either way
		return TokenPair{}, errInvalidCredentials
	} else if err != nil {
		return TokenPair{}, err
	}
	if bcrypt.CompareHashAndPassword([]byte(u.PasswordHash), []byte(password)) != nil {
		return TokenPair{}, errInvalidCredentials
	}
	return s.issue(ctx, q, auth.Principal{UserID: u.ID, OrgID: u.OrganizationID, Role: auth.Role(u.Role)}, uuid.New())
}

// Refresh rotates a refresh token. Presenting an already-rotated token means it was copied:
// the whole token family is revoked, logging out both the attacker and the user.
func (s *Service) Refresh(ctx context.Context, token string) (TokenPair, error) {
	var pair TokenPair
	var reused bool
	err := database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rt, err := q.GetRefreshTokenForUpdate(ctx, hashRefresh(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return errInvalidRefresh
		} else if err != nil {
			return err
		}
		if rt.RevokedAt != nil {
			reused = true
			return q.RevokeRefreshFamily(ctx, rt.FamilyID) // commit the revocation
		}
		if time.Now().After(rt.ExpiresAt) {
			return errInvalidRefresh
		}
		if err := q.RevokeRefreshToken(ctx, rt.ID); err != nil {
			return err
		}
		u, err := q.GetUser(ctx, rt.UserID) // current role, not the one at login
		if err != nil {
			return err
		}
		pair, err = s.issue(ctx, q, auth.Principal{UserID: u.ID, OrgID: u.OrganizationID, Role: auth.Role(u.Role)}, rt.FamilyID)
		return err
	})
	if err == nil && reused {
		return TokenPair{}, errInvalidRefresh
	}
	return pair, err
}

// Logout revokes the refresh token's whole family. Unknown tokens are ignored (idempotent).
func (s *Service) Logout(ctx context.Context, token string) error {
	return database.WithTx(ctx, s.pool, func(tx pgx.Tx) error {
		q := store.New(tx)
		rt, err := q.GetRefreshTokenForUpdate(ctx, hashRefresh(token))
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		} else if err != nil {
			return err
		}
		return q.RevokeRefreshFamily(ctx, rt.FamilyID)
	})
}

func (s *Service) Organization(ctx context.Context, orgID uuid.UUID) (Organization, error) {
	org, err := store.New(s.pool).GetOrganization(ctx, orgID)
	if errors.Is(err, pgx.ErrNoRows) {
		return Organization{}, httpx.NotFound("organization")
	}
	return toOrg(org), err
}

func (s *Service) ListUsers(ctx context.Context, orgID uuid.UUID) ([]User, error) {
	rows, err := store.New(s.pool).ListUsers(ctx, orgID)
	users := make([]User, len(rows))
	for i, r := range rows {
		users[i] = User(r)
	}
	return users, err
}

type CreateUserInput struct {
	Email    string `json:"email"`
	Password string `json:"password"`
	Role     string `json:"role"`
}

// CreateUser adds a user to the caller's organization (V1 stand-in for e-mail invitations).
func (s *Service) CreateUser(ctx context.Context, orgID uuid.UUID, in CreateUserInput) (User, error) {
	problems := map[string]string{}
	role := auth.Role(strings.ToUpper(in.Role))
	if role != auth.RoleAdmin && role != auth.RoleDispatcher && role != auth.RoleViewer {
		problems["role"] = "one of ADMIN, DISPATCHER, VIEWER"
	}
	email, hash, err := s.credentials(in.Email, in.Password, problems)
	if err != nil {
		return User{}, err
	}
	u, err := store.New(s.pool).CreateUser(ctx, store.CreateUserParams{
		OrganizationID: orgID, Email: email, PasswordHash: string(hash), Role: string(role),
	})
	if database.IsUniqueViolation(err, "users_organization_id_email_key") {
		return User{}, httpx.Conflict("EMAIL_TAKEN", "a user with this email already exists")
	}
	return User(u), err
}

// credentials validates email/password (adding to problems) and hashes the password.
func (s *Service) credentials(rawEmail, password string, problems map[string]string) (string, []byte, error) {
	email := normalizeEmail(rawEmail)
	if addr, err := mail.ParseAddress(email); err != nil || addr.Address != email {
		problems["email"] = "invalid email address"
	}
	if n := len(password); n < 10 || n > 72 { // bcrypt only uses the first 72 bytes
		problems["password"] = "10-72 characters"
	}
	if len(problems) > 0 {
		return "", nil, httpx.Unprocessable("VALIDATION_FAILED", "invalid input", problems)
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(password), s.bcryptCost)
	return email, hash, err
}

func (s *Service) issue(ctx context.Context, q *store.Queries, p auth.Principal, family uuid.UUID) (TokenPair, error) {
	access, exp, err := s.tokens.Issue(p)
	if err != nil {
		return TokenPair{}, err
	}
	refresh, hash := newRefreshToken()
	if err := q.InsertRefreshToken(ctx, store.InsertRefreshTokenParams{
		UserID: p.UserID, FamilyID: family, TokenHash: hash, ExpiresAt: time.Now().Add(s.refreshTTL),
	}); err != nil {
		return TokenPair{}, err
	}
	return TokenPair{access, refresh, "Bearer", int(time.Until(exp).Seconds())}, nil
}

func normalizeEmail(e string) string { return strings.ToLower(strings.TrimSpace(e)) }

func defaultStr(v, d string) string {
	if strings.TrimSpace(v) == "" {
		return d
	}
	return strings.TrimSpace(v)
}

func toOrg(o store.CoreOrganization) Organization {
	return Organization{o.ID, o.Name, o.Slug, o.Timezone, o.Currency, o.CreatedAt}
}
