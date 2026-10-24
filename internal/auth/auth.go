// Package auth authenticates callers (ADR-0007, ADR-0008).
//
// Tokens:
//
//   - access token: a short-lived JWT, verified without any I/O on every
//     request and turned into an identity.Principal by the Authenticate
//     middleware;
//   - refresh token: an opaque random string. Only its SHA-256 hash is
//     stored (auth_sessions). Each refresh rotates it; presenting an
//     already-rotated token revokes the whole session family, because it
//     means the token was stolen and used by someone else.
//
// Every collaborator is a port defined here. The composition root bridges
// UserFinder to the user module, so auth never imports user.
package auth

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
)

// Credentials is what auth needs to know about a user.
type Credentials struct {
	UserID       uuid.UUID
	PasswordHash string
	Role         identity.Role
}

// Session is one refresh token in a rotation family.
type Session struct {
	ID        uuid.UUID
	FamilyID  uuid.UUID // shared by every token descending from one login
	UserID    uuid.UUID
	TokenHash []byte
	ExpiresAt time.Time
	UsedAt    *time.Time // set when rotated; reuse after this is theft
	RevokedAt *time.Time
	UserAgent string
	IP        string
	CreatedAt time.Time
}

// Errors returned by this module.
var (
	ErrInvalidCredentials = errs.New(errs.Unauthenticated, "invalid email or password")
	ErrInvalidRefresh     = errs.New(errs.Unauthenticated, "invalid or expired refresh token")
	ErrSessionNotFound    = errs.New(errs.NotFound, "session not found")
)

// UserFinder looks users up. Implementations return an error of kind
// NotFound when the user does not exist.
type UserFinder interface {
	CredentialsByEmail(ctx context.Context, email string) (Credentials, error)
	CredentialsByID(ctx context.Context, id uuid.UUID) (Credentials, error)
}

// SessionStore persists refresh-token sessions.
type SessionStore interface {
	Create(ctx context.Context, s Session) error
	// ByTokenHashForUpdate locks the row until the transaction ends, so
	// concurrent refreshes of one token are serialized.
	ByTokenHashForUpdate(ctx context.Context, hash []byte) (Session, error)
	MarkUsed(ctx context.Context, id uuid.UUID, at time.Time) error
	RevokeFamily(ctx context.Context, familyID uuid.UUID, at time.Time) error
	RevokeFamilyOf(ctx context.Context, sessionID uuid.UUID, at time.Time) error
	RevokeUser(ctx context.Context, userID uuid.UUID, at time.Time) error
	DeleteExpired(ctx context.Context, before time.Time) (int64, error)
}

// TokenIssuer signs and verifies access tokens.
type TokenIssuer interface {
	Issue(p identity.Principal, ttl time.Duration) (token string, expiresAt time.Time, err error)
	Verify(token string) (identity.Principal, error)
}

// PasswordHasher verifies passwords. Hash is used once at startup to build
// the dummy hash that equalizes login timing for unknown emails.
type PasswordHasher interface {
	Hash(plain string) (string, error)
	Compare(hash, plain string) (bool, error)
}

// Transactor runs fn atomically.
type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// Denylist optionally revokes access tokens before they expire. It is only
// consulted when configured; without it, logout takes effect for access
// tokens when they expire (at most AccessTTL later).
type Denylist interface {
	Revoke(ctx context.Context, tokenID string, until time.Time) error
	IsRevoked(ctx context.Context, tokenID string) (bool, error)
}

// PurgeExpiredSessions is the periodic job that deletes dead sessions.
type PurgeExpiredSessions struct{}

// Kind implements jobs.Args.
func (PurgeExpiredSessions) Kind() string { return "auth.purge_expired_sessions" }
