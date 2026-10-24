package main

import (
	"context"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/auth"
	"github.com/islamghany/goplate/internal/user"
)

// Cross-module bridges live here, in the composition root.
//
// Modules never import each other (ADR-0002). When one needs another, it
// declares a small interface in its own terms (auth.UserFinder), and a
// bridge like this one translates. If users moved to a separate service,
// only this adapter would change, to an HTTP or gRPC client.

// authUsers adapts the user module to auth.UserFinder.
type authUsers struct {
	users *user.Service
}

var _ auth.UserFinder = authUsers{}

func (a authUsers) CredentialsByEmail(ctx context.Context, email string) (auth.Credentials, error) {
	u, err := a.users.ByEmail(ctx, email)
	if err != nil {
		return auth.Credentials{}, err // user.ErrNotFound carries Kind NotFound
	}
	return credentials(u), nil
}

func (a authUsers) CredentialsByID(ctx context.Context, id uuid.UUID) (auth.Credentials, error) {
	u, err := a.users.ByID(ctx, id)
	if err != nil {
		return auth.Credentials{}, err
	}
	return credentials(u), nil
}

func credentials(u user.User) auth.Credentials {
	return auth.Credentials{UserID: u.ID, PasswordHash: u.PasswordHash, Role: u.Role}
}
