package auth

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/web"
)

// Deps are the ports this module needs.
type Deps struct {
	Users    UserFinder
	Sessions SessionStore
	Tokens   TokenIssuer
	Hasher   PasswordHasher
	Tx       Transactor
	Denylist Denylist       // optional
	Throttle *LoginThrottle // optional
	Log      *slog.Logger
}

// ModuleConfig combines token lifetimes and transport settings.
type ModuleConfig struct {
	Tokens    Config
	Transport TransportConfig
}

// Module is the auth module's public face.
type Module struct {
	Service *Service
	handler *Handler
}

// NewModule wires the module.
func NewModule(d Deps, cfg ModuleConfig) (*Module, error) {
	svc, err := NewService(d.Users, d.Sessions, d.Tokens, d.Hasher, d.Tx, d.Denylist, d.Log, cfg.Tokens)
	if err != nil {
		return nil, err
	}
	return &Module{Service: svc, handler: NewHandler(svc, cfg.Transport, d.Throttle, d.Log)}, nil
}

// Routes registers the module's endpoints; see Handler.Routes.
func (m *Module) Routes(public, login, protected *web.Router) {
	m.handler.Routes(public, login, protected)
}

// Authenticate returns the middleware that protects routes: it verifies the
// access token and puts the caller's identity.Principal in the context.
func (m *Module) Authenticate() web.Middleware { return m.handler.Authenticate }

// CacheDenylist implements Denylist on any cache.Store (Redis in
// production). Entries expire when the token would have expired anyway.
type CacheDenylist struct {
	store cache.Store
}

// NewCacheDenylist returns a Denylist backed by store.
func NewCacheDenylist(store cache.Store) *CacheDenylist { return &CacheDenylist{store: store} }

func (d *CacheDenylist) Revoke(ctx context.Context, tokenID string, until time.Time) error {
	ttl := time.Until(until)
	if ttl <= 0 {
		return nil
	}
	return d.store.Set(ctx, "auth:deny:"+tokenID, []byte{1}, ttl)
}

func (d *CacheDenylist) IsRevoked(ctx context.Context, tokenID string) (bool, error) {
	_, err := d.store.Get(ctx, "auth:deny:"+tokenID)
	switch {
	case errors.Is(err, cache.ErrMiss):
		return false, nil
	case err != nil:
		return false, err
	}
	return true, nil
}
