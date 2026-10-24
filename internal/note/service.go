package note

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/cache"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/validate"
)

// Limits enforced on note content.
const (
	maxTitle  = 200
	maxBody   = 10_000
	maxTags   = 10
	maxTagLen = 32
)

// Config tunes the module.
type Config struct {
	CacheTTL time.Duration
}

// Service implements the note use cases. Every method resolves the caller
// from the context and scopes all data access to them.
type Service struct {
	repo  Repository
	tx    Transactor
	jobs  JobEnqueuer
	cache *cache.Loader[Note]
	log   *slog.Logger
	now   func() time.Time
}

// NewService returns a Service.
func NewService(repo Repository, tx Transactor, jobs JobEnqueuer, store cache.Store, log *slog.Logger, cfg Config) *Service {
	return &Service{
		repo:  repo,
		tx:    tx,
		jobs:  jobs,
		cache: cache.NewLoader[Note](store, cfg.CacheTTL),
		log:   log,
		now:   time.Now,
	}
}

// CreateInput holds the fields of a new note.
type CreateInput struct {
	Title string
	Body  string
	Tags  []string
}

// Create stores a note and enqueues a Created job in the same transaction:
// either both happen or neither does.
func (s *Service) Create(ctx context.Context, in CreateInput) (Note, error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return Note{}, err
	}
	n := Note{Title: strings.TrimSpace(in.Title), Body: in.Body, Tags: normalizeTags(in.Tags)}
	if err := validateContent(n); err != nil {
		return Note{}, err
	}
	now := s.now().UTC().Truncate(time.Microsecond)
	n.ID, n.OwnerID, n.CreatedAt, n.UpdatedAt, n.Version = uuid.Must(uuid.NewV7()), p.UserID, now, now, 1

	var created Note
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		var err error
		if created, err = s.repo.Create(ctx, n); err != nil {
			return err
		}
		return s.jobs.Enqueue(ctx, Created{NoteID: created.ID, OwnerID: created.OwnerID})
	})
	return created, err
}

// Get returns one of the caller's notes, served from cache when possible.
func (s *Service) Get(ctx context.Context, id uuid.UUID) (Note, error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return Note{}, err
	}
	return s.cache.GetOrLoad(ctx, cacheKey(p.UserID, id), func(ctx context.Context) (Note, error) {
		return s.repo.Get(ctx, p.UserID, id)
	})
}

// List pages through the caller's notes.
func (s *Service) List(ctx context.Context, f Filter, req page.Request) (page.Page[Note], error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return page.Page[Note]{}, err
	}
	f.Tag = strings.ToLower(strings.TrimSpace(f.Tag))
	f.Search = strings.TrimSpace(f.Search)
	v := validate.New()
	v.MaxLen("tag", f.Tag, maxTagLen)
	v.MaxLen("q", f.Search, 100)
	if err := v.Err(); err != nil {
		return page.Page[Note]{}, err
	}

	rows, err := s.repo.List(ctx, p.UserID, f, req)
	if err != nil {
		return page.Page[Note]{}, err
	}
	return page.Build(rows, req.Limit, func(n Note) page.Cursor {
		return page.Cursor{CreatedAt: n.CreatedAt, ID: n.ID}
	}), nil
}

// UpdateInput is a partial update: nil fields are left unchanged.
// IfMatch, when set, must equal the stored version (optimistic locking).
type UpdateInput struct {
	Title   *string
	Body    *string
	Tags    *[]string
	IfMatch *int
}

// Update applies a partial update. The row is locked for the duration of
// the transaction, so concurrent updates are serialized instead of silently
// overwriting each other.
func (s *Service) Update(ctx context.Context, id uuid.UUID, in UpdateInput) (Note, error) {
	p, err := identity.Require(ctx)
	if err != nil {
		return Note{}, err
	}

	var updated Note
	err = s.tx.WithinTx(ctx, func(ctx context.Context) error {
		n, err := s.repo.GetForUpdate(ctx, p.UserID, id)
		if err != nil {
			return err
		}
		if in.IfMatch != nil && *in.IfMatch != n.Version {
			return ErrVersionMismatch
		}
		if in.Title != nil {
			n.Title = strings.TrimSpace(*in.Title)
		}
		if in.Body != nil {
			n.Body = *in.Body
		}
		if in.Tags != nil {
			n.Tags = normalizeTags(*in.Tags)
		}
		if err := validateContent(n); err != nil {
			return err
		}
		n.UpdatedAt = s.now().UTC().Truncate(time.Microsecond)
		updated, err = s.repo.Update(ctx, n)
		return err
	})
	if err != nil {
		return Note{}, err
	}
	s.cache.Invalidate(ctx, cacheKey(p.UserID, id)) // after commit, never before
	return updated, nil
}

// Delete removes one of the caller's notes.
func (s *Service) Delete(ctx context.Context, id uuid.UUID) error {
	p, err := identity.Require(ctx)
	if err != nil {
		return err
	}
	if err := s.repo.Delete(ctx, p.UserID, id); err != nil {
		return err
	}
	s.cache.Invalidate(ctx, cacheKey(p.UserID, id))
	return nil
}

// HandleCreated processes the Created job. Jobs are delivered at least
// once, so handlers must be idempotent: running this twice for the same
// note must be harmless. Here we only log; a real handler might upsert a
// search document keyed by NoteID, which is naturally idempotent.
func (s *Service) HandleCreated(ctx context.Context, job Created) error {
	s.log.InfoContext(ctx, "note created", "note_id", job.NoteID, "owner_id", job.OwnerID)
	return nil
}

func validateContent(n Note) error {
	v := validate.New()
	v.Required("title", n.Title)
	v.MaxLen("title", n.Title, maxTitle)
	v.MaxLen("body", n.Body, maxBody)
	v.Check(len(n.Tags) <= maxTags, "tags", fmt.Sprintf("must contain at most %d tags", maxTags))
	for _, t := range n.Tags {
		v.Check(len(t) <= maxTagLen, "tags", fmt.Sprintf("each tag must be at most %d characters", maxTagLen))
	}
	return v.Err()
}

// normalizeTags lower-cases, trims, drops empties and de-duplicates.
func normalizeTags(tags []string) []string {
	out := make([]string, 0, len(tags))
	for _, t := range tags {
		t = strings.ToLower(strings.TrimSpace(t))
		if t != "" && !slices.Contains(out, t) {
			out = append(out, t)
		}
	}
	return out
}

// cacheKey is versioned ("v1") so changing Note's shape can invalidate all
// cached entries at once by bumping it.
func cacheKey(owner, id uuid.UUID) string {
	return "note:v1:" + owner.String() + ":" + id.String()
}
