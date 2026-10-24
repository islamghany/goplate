package user

import (
	"context"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/validate"
)

// Service implements the user use cases.
//
// Services validate their own inputs and enforce authorization themselves:
// they are the module's API for every caller (HTTP today, jobs or a CLI
// tomorrow), so correctness must not depend on the transport.
type Service struct {
	repo   Repository
	hasher PasswordHasher
	now    func() time.Time
}

// NewService returns a Service.
func NewService(repo Repository, hasher PasswordHasher) *Service {
	return &Service{repo: repo, hasher: hasher, now: time.Now}
}

// RegisterInput is the data needed to create an account.
type RegisterInput struct {
	Email    string
	Name     string
	Password string
}

func (in *RegisterInput) normalize() {
	in.Email = strings.ToLower(strings.TrimSpace(in.Email))
	in.Name = strings.TrimSpace(in.Name)
}

func (in RegisterInput) validate() error {
	v := validate.New()
	v.Required("email", in.Email)
	v.Email("email", in.Email)
	v.MaxLen("email", in.Email, 254)
	v.Required("name", in.Name)
	v.MaxLen("name", in.Name, 100)
	// NIST SP 800-63B: favour length over composition rules.
	v.MinLen("password", in.Password, 12)
	v.MaxLen("password", in.Password, 128)
	return v.Err()
}

// Register creates a regular user account.
func (s *Service) Register(ctx context.Context, in RegisterInput) (User, error) {
	in.normalize()
	if err := in.validate(); err != nil {
		return User{}, err
	}
	hash, err := s.hasher.Hash(in.Password)
	if err != nil {
		return User{}, errs.Wrap(errs.Internal, "", err)
	}
	now := s.now().UTC().Truncate(time.Microsecond) // Postgres precision
	return s.repo.Create(ctx, User{
		ID:           uuid.Must(uuid.NewV7()),
		Email:        in.Email,
		Name:         in.Name,
		PasswordHash: hash,
		Role:         identity.RoleUser,
		CreatedAt:    now,
		UpdatedAt:    now,
	})
}

// Me returns the calling user.
func (s *Service) Me(ctx context.Context) (User, error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return User{}, err
	}
	return s.repo.ByID(ctx, p.UserID)
}

// Get returns a user. Callers may read themselves; admins may read anyone.
// Everyone else gets NotFound rather than Forbidden, so the API does not
// reveal which IDs exist.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (User, error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return User{}, err
	}
	if p.UserID != id && !p.IsAdmin() {
		return User{}, ErrNotFound
	}
	return s.repo.ByID(ctx, id)
}

// UpdateMe changes the caller's display name.
func (s *Service) UpdateMe(ctx context.Context, name string) (User, error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return User{}, err
	}
	name = strings.TrimSpace(name)
	v := validate.New()
	v.Required("name", name)
	v.MaxLen("name", name, 100)
	if err := v.Err(); err != nil {
		return User{}, err
	}
	return s.repo.UpdateName(ctx, p.UserID, name, s.now().UTC().Truncate(time.Microsecond))
}

// ErrAdminOnly is returned when a non-admin calls an admin use case.
var ErrAdminOnly = errs.New(errs.PermissionDenied, "this action requires the admin role")

// List pages through all users. Admin only.
func (s *Service) List(ctx context.Context, req page.Request) (page.Page[User], error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return page.Page[User]{}, err
	}
	if !p.IsAdmin() {
		return page.Page[User]{}, ErrAdminOnly
	}
	rows, err := s.repo.List(ctx, req)
	if err != nil {
		return page.Page[User]{}, err
	}
	return page.Build(rows, req.Limit, func(u User) page.Cursor {
		return page.Cursor{CreatedAt: u.CreatedAt, ID: u.ID}
	}), nil
}

// ByEmail looks a user up without authorization checks. It exists for other
// modules (auth) via the composition root, never for HTTP handlers.
func (s *Service) ByEmail(ctx context.Context, email string) (User, error) {
	return s.repo.ByEmail(ctx, strings.ToLower(strings.TrimSpace(email)))
}

// ByID looks a user up without authorization checks; see ByEmail.
func (s *Service) ByID(ctx context.Context, id uuid.UUID) (User, error) {
	return s.repo.ByID(ctx, id)
}
