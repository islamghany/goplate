package user

import "github.com/islamghany/goplate/internal/web"

// Deps are the ports this module needs. The composition root (cmd/api)
// fills them with concrete adapters; tests fill them with fakes.
type Deps struct {
	Repo   Repository
	Hasher PasswordHasher
}

// Module is the user module's public face: its Service (for other modules,
// bridged by the composition root) and its HTTP routes.
type Module struct {
	Service *Service
	handler *Handler
}

// NewModule wires the module from its dependencies.
func NewModule(d Deps) *Module {
	svc := NewService(d.Repo, d.Hasher)
	return &Module{Service: svc, handler: NewHandler(svc)}
}

// Routes registers the module's HTTP endpoints.
func (m *Module) Routes(public, protected *web.Router) { m.handler.Routes(public, protected) }
