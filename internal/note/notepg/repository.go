// Package notepg is the Postgres adapter for the note module.
// Generated sqlc code lives next to this file and never leaves the package.
package notepg

import (
	"context"
	"strings"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/note"
	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/platform/postgres"
)

// Repository implements note.Repository.
type Repository struct {
	txm *postgres.TxManager
}

// NewRepository returns a Repository.
func NewRepository(txm *postgres.TxManager) *Repository {
	return &Repository{txm: txm}
}

var _ note.Repository = (*Repository)(nil)

func (r *Repository) q(ctx context.Context) *Queries { return New(r.txm.DB(ctx)) }

func (r *Repository) Create(ctx context.Context, n note.Note) (note.Note, error) {
	row, err := r.q(ctx).InsertNote(ctx, InsertNoteParams{
		ID:        n.ID,
		OwnerID:   n.OwnerID,
		Title:     n.Title,
		Body:      n.Body,
		Tags:      nonNil(n.Tags),
		CreatedAt: n.CreatedAt,
	})
	return one(row, err)
}

func (r *Repository) Get(ctx context.Context, owner, id uuid.UUID) (note.Note, error) {
	row, err := r.q(ctx).GetNote(ctx, GetNoteParams{ID: id, OwnerID: owner})
	return one(row, err)
}

func (r *Repository) GetForUpdate(ctx context.Context, owner, id uuid.UUID) (note.Note, error) {
	row, err := r.q(ctx).GetNoteForUpdate(ctx, GetNoteForUpdateParams{ID: id, OwnerID: owner})
	return one(row, err)
}

func (r *Repository) Update(ctx context.Context, n note.Note) (note.Note, error) {
	row, err := r.q(ctx).UpdateNote(ctx, UpdateNoteParams{
		ID:        n.ID,
		OwnerID:   n.OwnerID,
		Title:     n.Title,
		Body:      n.Body,
		Tags:      nonNil(n.Tags),
		UpdatedAt: n.UpdatedAt,
	})
	return one(row, err)
}

func (r *Repository) Delete(ctx context.Context, owner, id uuid.UUID) error {
	n, err := r.q(ctx).DeleteNote(ctx, DeleteNoteParams{ID: id, OwnerID: owner})
	switch {
	case err != nil:
		return errs.Wrap(errs.Internal, "", err)
	case n == 0:
		return note.ErrNotFound
	}
	return nil
}

func (r *Repository) List(ctx context.Context, owner uuid.UUID, f note.Filter, req page.Request) ([]note.Note, error) {
	params := ListNotesParams{OwnerID: owner, RowLimit: int32(req.Limit + 1)} //nolint:gosec // Limit <= page.MaxLimit
	if f.Tag != "" {
		params.Tag = &f.Tag
	}
	if f.Search != "" {
		pattern := "%" + escapeLike(f.Search) + "%"
		params.Pattern = &pattern
	}
	if req.After != nil {
		params.AfterCreatedAt, params.AfterID = &req.After.CreatedAt, &req.After.ID
	}
	rows, err := r.q(ctx).ListNotes(ctx, params)
	if err != nil {
		return nil, errs.Wrap(errs.Internal, "", err)
	}
	out := make([]note.Note, len(rows))
	for i, row := range rows {
		out[i] = toDomain(row)
	}
	return out, nil
}

// escapeLike makes user input literal inside an ILIKE pattern, so searching
// for "100%" does not match everything.
func escapeLike(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

func one(row Note, err error) (note.Note, error) {
	switch {
	case postgres.IsNoRows(err):
		return note.Note{}, note.ErrNotFound
	case err != nil:
		return note.Note{}, errs.Wrap(errs.Internal, "", err)
	}
	return toDomain(row), nil
}

func toDomain(row Note) note.Note {
	return note.Note{
		ID:        row.ID,
		OwnerID:   row.OwnerID,
		Title:     row.Title,
		Body:      row.Body,
		Tags:      row.Tags,
		Version:   int(row.Version),
		CreatedAt: row.CreatedAt,
		UpdatedAt: row.UpdatedAt,
	}
}

func nonNil(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
