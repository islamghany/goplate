// Package note is the reference CRUD module: copy it when adding a new one
// (see docs/adding-a-module.md). It demonstrates, end to end:
//
//   - ownership authorization inside the service (not the handler);
//   - keyset pagination and optional filters;
//   - optimistic concurrency with ETag / If-Match;
//   - a transaction spanning a row lock, an update and a job enqueue;
//   - cache-aside reads with invalidation on write;
//   - an idempotent background job handler.
package note

import (
	"context"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/jobs"
	"github.com/islamghany/goplate/internal/page"
)

// Note is a user's note.
type Note struct {
	ID        uuid.UUID `json:"id"`
	OwnerID   uuid.UUID `json:"owner_id"`
	Title     string    `json:"title"`
	Body      string    `json:"body"`
	Tags      []string  `json:"tags"`
	Version   int       `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// The json tags above are for the cache encoding only; the HTTP layer has
// its own response type.

// Errors returned by this module.
var (
	ErrNotFound        = errs.New(errs.NotFound, "note not found")
	ErrVersionMismatch = errs.New(errs.PreconditionFailed, "the note was modified by someone else; reload it and retry")
)

// Filter narrows a listing. Zero values mean "no filter".
type Filter struct {
	Tag    string
	Search string
}

// Repository persists notes. Every method is scoped to an owner so a
// forgotten check cannot leak another user's notes.
type Repository interface {
	Create(ctx context.Context, n Note) (Note, error)
	Get(ctx context.Context, owner, id uuid.UUID) (Note, error)
	// GetForUpdate locks the row until the surrounding transaction ends.
	GetForUpdate(ctx context.Context, owner, id uuid.UUID) (Note, error)
	// Update saves title/body/tags, bumps Version and sets UpdatedAt.
	Update(ctx context.Context, n Note) (Note, error)
	Delete(ctx context.Context, owner, id uuid.UUID) error
	// List returns up to req.Limit+1 notes, newest first.
	List(ctx context.Context, owner uuid.UUID, f Filter, req page.Request) ([]Note, error)
}

// Transactor runs fn atomically (implemented by platform/postgres).
type Transactor interface {
	WithinTx(ctx context.Context, fn func(ctx context.Context) error) error
}

// JobEnqueuer queues background work. Inside WithinTx, the job commits or
// rolls back with the transaction (implemented by platform/riverjobs).
type JobEnqueuer interface {
	Enqueue(ctx context.Context, args jobs.Args) error
}

// Created is the job enqueued after a note is created. Real projects would
// index it for search, notify collaborators, etc.
type Created struct {
	NoteID  uuid.UUID `json:"note_id"`
	OwnerID uuid.UUID `json:"owner_id"`
}

// Kind implements jobs.Args. Never change it: it is stored with queued jobs.
func (Created) Kind() string { return "note.created" }
