package user

import (
	"net/http"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/web"
)

// Handler exposes the user use cases over HTTP.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers endpoints. public routes need no authentication;
// protected routes run behind the auth middleware.
func (h *Handler) Routes(public, protected *web.Router) {
	public.Handle("POST /users", h.register)
	protected.Handle("GET /users/me", h.me)
	protected.Handle("PATCH /users/me", h.updateMe)
	protected.Handle("GET /users/{id}", h.get)
	protected.Handle("GET /users", h.list)
}

// userResponse is the public representation of a User. Keeping it separate
// from the domain type means a new internal field can never leak by accident
// (PasswordHash has no json tag to forget).
type userResponse struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Name      string    `json:"name"`
	Role      string    `json:"role"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toResponse(u User) userResponse {
	return userResponse{ID: u.ID, Email: u.Email, Name: u.Name, Role: string(u.Role), CreatedAt: u.CreatedAt, UpdatedAt: u.UpdatedAt}
}

type registerRequest struct {
	Email    string `json:"email"`
	Name     string `json:"name"`
	Password string `json:"password"`
}

func (h *Handler) register(w http.ResponseWriter, r *http.Request) error {
	req, err := web.Decode[registerRequest](r)
	if err != nil {
		return err
	}
	u, err := h.svc.Register(r.Context(), RegisterInput(req))
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/users/"+u.ID.String())
	return web.JSON(w, http.StatusCreated, toResponse(u))
}

func (h *Handler) me(w http.ResponseWriter, r *http.Request) error {
	u, err := h.svc.Me(r.Context())
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, toResponse(u))
}

type updateMeRequest struct {
	Name string `json:"name"`
}

func (h *Handler) updateMe(w http.ResponseWriter, r *http.Request) error {
	req, err := web.Decode[updateMeRequest](r)
	if err != nil {
		return err
	}
	u, err := h.svc.UpdateMe(r.Context(), req.Name)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, toResponse(u))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := web.PathUUID(r, "id")
	if err != nil {
		return err
	}
	u, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	return web.JSON(w, http.StatusOK, toResponse(u))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, err := web.QueryInt(r, "limit", page.DefaultLimit)
	if err != nil {
		return err
	}
	req, err := page.NewRequest(limit, r.URL.Query().Get("cursor"))
	if err != nil {
		return err
	}
	p, err := h.svc.List(r.Context(), req)
	if err != nil {
		return err
	}
	items := make([]userResponse, len(p.Items))
	for i, u := range p.Items {
		items[i] = toResponse(u)
	}
	return web.JSON(w, http.StatusOK, page.Page[userResponse]{Items: items, NextCursor: p.NextCursor})
}
