package note

import (
	"log/slog"

	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/web"
)

// Deps are the ports this module needs.
type Deps struct {
	Repo  Repository
	Tx    Transactor
	Jobs  JobEnqueuer
	Cache cache.Store
	Log   *slog.Logger
}

// Module is the note module's public face.
type Module struct {
	Service *Service
	handler *Handler
}

// NewModule wires the module.
func NewModule(d Deps, cfg Config) *Module {
	svc := NewService(d.Repo, d.Tx, d.Jobs, d.Cache, d.Log, cfg)
	return &Module{Service: svc, handler: NewHandler(svc)}
}

// Routes registers the module's endpoints.
func (m *Module) Routes(protected *web.Router) { m.handler.Routes(protected) }
