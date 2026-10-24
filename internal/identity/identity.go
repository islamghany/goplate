// Package identity carries "who is calling" through a request's context.
//
// It is a shared kernel package: the auth module puts a Principal into the
// context after verifying a token, and any other module (note, user, ...)
// reads it for authorization decisions without importing auth. That keeps
// modules decoupled (ADR-0002) while agreeing on one small type.
package identity

import (
	"context"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
)

// Role is a coarse-grained permission level.
type Role string

// The roles known to the system.
const (
	RoleUser  Role = "user"
	RoleAdmin Role = "admin"
)

// Valid reports whether r is a known role.
func (r Role) Valid() bool { return r == RoleUser || r == RoleAdmin }

// Principal is the authenticated caller.
type Principal struct {
	UserID    uuid.UUID
	SessionID uuid.UUID
	Role      Role
	TokenID   string // the access token's jti, used for optional revocation
	ExpiresAt int64  // the access token's exp (unix seconds)
}

// IsAdmin reports whether the caller has the admin role.
func (p Principal) IsAdmin() bool { return p.Role == RoleAdmin }

type ctxKey struct{}

// WithPrincipal returns a copy of ctx carrying p.
func WithPrincipal(ctx context.Context, p Principal) context.Context {
	return context.WithValue(ctx, ctxKey{}, p)
}

// FromContext returns the Principal in ctx, if any.
func FromContext(ctx context.Context) (Principal, bool) {
	p, ok := ctx.Value(ctxKey{}).(Principal)
	return p, ok
}

// ErrUnauthenticated is returned by Require when no principal is present.
var ErrUnauthenticated = errs.New(errs.Unauthenticated, "authentication required")

// Require returns the Principal in ctx or an Unauthenticated error. Services
// call it so they stay correct even if a route forgot its auth middleware.
func Require(ctx context.Context) (Principal, error) {
	p, ok := FromContext(ctx)
	if !ok {
		return Principal{}, ErrUnauthenticated
	}
	return p, nil
}
