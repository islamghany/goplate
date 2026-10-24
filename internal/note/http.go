package note

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/web"
)

// Handler exposes notes over HTTP. Handlers stay thin: decode, call the
// service, encode. No business rules live here.
type Handler struct {
	svc *Service
}

// NewHandler returns a Handler.
func NewHandler(svc *Service) *Handler { return &Handler{svc: svc} }

// Routes registers endpoints; every note route requires authentication.
func (h *Handler) Routes(protected *web.Router) {
	protected.Handle("POST /notes", h.create)
	protected.Handle("GET /notes", h.list)
	protected.Handle("GET /notes/{id}", h.get)
	protected.Handle("PATCH /notes/{id}", h.update)
	protected.Handle("DELETE /notes/{id}", h.delete)
}

type noteResponse struct {
	ID        uuid.UUID `json:"id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Tags      []string  `json:"tags"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

func toResponse(n Note) noteResponse {
	tags := n.Tags
	if tags == nil {
		tags = []string{}
	}
	return noteResponse{ID: n.ID, Title: n.Title, Body: n.Body, Tags: tags, Version: n.Version,
		CreatedAt: n.CreatedAt, UpdatedAt: n.UpdatedAt}
}

// etag renders the version as a strong ETag, e.g. "3".
func etag(n Note) string { return `"` + strconv.Itoa(n.Version) + `"` }

type createRequest struct {
	Title string   `json:"title"`
	Body  string   `json:"body"`
	Tags  []string `json:"tags"`
}

func (h *Handler) create(w http.ResponseWriter, r *http.Request) error {
	req, err := web.Decode[createRequest](r)
	if err != nil {
		return err
	}
	n, err := h.svc.Create(r.Context(), CreateInput(req))
	if err != nil {
		return err
	}
	w.Header().Set("Location", "/v1/notes/"+n.ID.String())
	w.Header().Set("ETag", etag(n))
	return web.JSON(w, http.StatusCreated, toResponse(n))
}

func (h *Handler) get(w http.ResponseWriter, r *http.Request) error {
	id, err := web.PathUUID(r, "id")
	if err != nil {
		return err
	}
	n, err := h.svc.Get(r.Context(), id)
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(n))
	return web.JSON(w, http.StatusOK, toResponse(n))
}

func (h *Handler) list(w http.ResponseWriter, r *http.Request) error {
	limit, err := web.QueryInt(r, "limit", page.DefaultLimit)
	if err != nil {
		return err
	}
	q := r.URL.Query()
	req, err := page.NewRequest(limit, q.Get("cursor"))
	if err != nil {
		return err
	}
	p, err := h.svc.List(r.Context(), Filter{Tag: q.Get("tag"), Search: q.Get("q")}, req)
	if err != nil {
		return err
	}
	items := make([]noteResponse, len(p.Items))
	for i, n := range p.Items {
		items[i] = toResponse(n)
	}
	return web.JSON(w, http.StatusOK, page.Page[noteResponse]{Items: items, NextCursor: p.NextCursor})
}

type updateRequest struct {
	Title *string   `json:"title"`
	Body  *string   `json:"body"`
	Tags  *[]string `json:"tags"`
}

func (h *Handler) update(w http.ResponseWriter, r *http.Request) error {
	id, err := web.PathUUID(r, "id")
	if err != nil {
		return err
	}
	ifMatch, err := parseIfMatch(r.Header.Get("If-Match"))
	if err != nil {
		return err
	}
	req, err := web.Decode[updateRequest](r)
	if err != nil {
		return err
	}
	n, err := h.svc.Update(r.Context(), id, UpdateInput{Title: req.Title, Body: req.Body, Tags: req.Tags, IfMatch: ifMatch})
	if err != nil {
		return err
	}
	w.Header().Set("ETag", etag(n))
	return web.JSON(w, http.StatusOK, toResponse(n))
}

func (h *Handler) delete(w http.ResponseWriter, r *http.Request) error {
	id, err := web.PathUUID(r, "id")
	if err != nil {
		return err
	}
	if err := h.svc.Delete(r.Context(), id); err != nil {
		return err
	}
	return web.NoContent(w)
}

// parseIfMatch reads a single strong ETag like "3". An absent header means
// "update unconditionally".
func parseIfMatch(v string) (*int, error) {
	if v == "" {
		return nil, nil
	}
	n, err := strconv.Atoi(strings.Trim(strings.TrimSpace(v), `"`))
	if err != nil {
		return nil, errs.New(errs.Invalid, `If-Match must be an ETag returned by this API, e.g. "3"`)
	}
	return &n, nil
}
