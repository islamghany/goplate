// Package user owns user accounts: registration, profiles and the admin
// listing.
//
// Layout (the same in every module, see ADR-0002 and docs/adding-a-module.md):
//
//	user.go     domain types, errors, and the ports this module needs
//	service.go  business rules; depends only on the ports below
//	http.go     HTTP transport: routes, DTOs, mapping
//	userpg/     Postgres adapter implementing Repository (sqlc inside)
//
// This package must not import other modules, platform adapters or drivers.
// Other modules that need users (auth) declare their own interfaces, and the
// composition root (cmd/api) bridges them to this module's Service.
package user

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/page"
)

// User is a registered account.
type User struct {
	ID           uuid.UUID
	Email        string
	Name         string
	PasswordHash string
	Role         identity.Role
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// Errors returned by this module. Their Kind decides the HTTP status.
var (
	ErrNotFound   = errs.New(errs.NotFound, "user not found")
	ErrEmailTaken = errs.New(errs.Conflict, "a user with this email already exists")
)

// Repository persists users. Implementations return ErrNotFound and
// ErrEmailTaken rather than driver errors.
type Repository interface {
	Create(ctx context.Context, u User) (User, error)
	ByID(ctx context.Context, id uuid.UUID) (User, error)
	ByEmail(ctx context.Context, email string) (User, error)
	UpdateName(ctx context.Context, id uuid.UUID, name string, at time.Time) (User, error)
	// List returns up to req.Limit+1 users, newest first, so the caller can
	// tell whether another page exists.
	List(ctx context.Context, req page.Request) ([]User, error)
}

// PasswordHasher hashes new passwords.
type PasswordHasher interface {
	Hash(plain string) (string, error)
}
