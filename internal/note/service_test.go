package note_test

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/jobs"
	"github.com/islamghany/goplate/internal/note"
	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/platform/memory"
)

// fakeRepo is an in-memory note.Repository.
type fakeRepo struct {
	mu    sync.Mutex
	notes map[uuid.UUID]note.Note
}

func newRepo() *fakeRepo { return &fakeRepo{notes: map[uuid.UUID]note.Note{}} }

func (r *fakeRepo) Create(_ context.Context, n note.Note) (note.Note, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.notes[n.ID] = n
	return n, nil
}

func (r *fakeRepo) Get(_ context.Context, owner, id uuid.UUID) (note.Note, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n, ok := r.notes[id]
	if !ok || n.OwnerID != owner {
		return note.Note{}, note.ErrNotFound
	}
	return n, nil
}

func (r *fakeRepo) GetForUpdate(ctx context.Context, owner, id uuid.UUID) (note.Note, error) {
	return r.Get(ctx, owner, id)
}

func (r *fakeRepo) Update(_ context.Context, n note.Note) (note.Note, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	n.Version++
	r.notes[n.ID] = n
	return n, nil
}

func (r *fakeRepo) Delete(ctx context.Context, owner, id uuid.UUID) error {
	if _, err := r.Get(ctx, owner, id); err != nil {
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.notes, id)
	return nil
}

func (r *fakeRepo) List(_ context.Context, owner uuid.UUID, f note.Filter, req page.Request) ([]note.Note, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []note.Note
	for _, n := range r.notes {
		if n.OwnerID == owner && (f.Tag == "" || slices.Contains(n.Tags, f.Tag)) {
			out = append(out, n)
		}
	}
	slices.SortFunc(out, func(a, b note.Note) int { return b.CreatedAt.Compare(a.CreatedAt) })
	if req.After != nil {
		i := slices.IndexFunc(out, func(n note.Note) bool { return n.ID == req.After.ID })
		out = out[i+1:]
	}
	return out[:min(len(out), req.Limit+1)], nil
}

// fakeTx records whether work ran inside a transaction and can fail commit.
type fakeTx struct{ failCommit bool }

type inTxKey struct{}

func (t fakeTx) WithinTx(ctx context.Context, fn func(context.Context) error) error {
	if err := fn(context.WithValue(ctx, inTxKey{}, true)); err != nil {
		return err
	}
	if t.failCommit {
		return errors.New("commit failed")
	}
	return nil
}

type fakeJobs struct {
	enqueued []jobs.Args
	inTx     []bool
}

func (j *fakeJobs) Enqueue(ctx context.Context, a jobs.Args) error {
	j.enqueued = append(j.enqueued, a)
	j.inTx = append(j.inTx, ctx.Value(inTxKey{}) == true)
	return nil
}

func newService(repo note.Repository, tx note.Transactor, j note.JobEnqueuer) *note.Service {
	return note.NewService(repo, tx, j, memory.NewCache(), slog.New(slog.DiscardHandler), note.Config{CacheTTL: time.Minute})
}

func as(ctx context.Context, user uuid.UUID) context.Context {
	return identity.WithPrincipal(ctx, identity.Principal{UserID: user, Role: identity.RoleUser})
}

func TestCreateEnqueuesJobInsideTransaction(t *testing.T) {
	j := &fakeJobs{}
	svc := newService(newRepo(), fakeTx{}, j)
	alice := uuid.New()

	n, err := svc.Create(as(t.Context(), alice), note.CreateInput{Title: " Groceries ", Tags: []string{"Home", "home", " "}})
	if err != nil {
		t.Fatal(err)
	}
	if n.Title != "Groceries" || !slices.Equal(n.Tags, []string{"home"}) || n.Version != 1 || n.OwnerID != alice {
		t.Fatalf("note = %+v", n)
	}
	if len(j.enqueued) != 1 || !j.inTx[0] {
		t.Fatalf("job must be enqueued inside the transaction: %+v", j)
	}
	if got := j.enqueued[0].(note.Created); got.NoteID != n.ID {
		t.Fatalf("job = %+v", got)
	}
}

func TestCreateValidates(t *testing.T) {
	svc := newService(newRepo(), fakeTx{}, &fakeJobs{})
	_, err := svc.Create(as(t.Context(), uuid.New()), note.CreateInput{Title: ""})
	if !errs.Is(err, errs.Invalid) {
		t.Fatalf("err = %v, want Invalid", err)
	}
}

func TestRequiresAuthentication(t *testing.T) {
	svc := newService(newRepo(), fakeTx{}, &fakeJobs{})
	if _, err := svc.Create(t.Context(), note.CreateInput{Title: "x"}); !errs.Is(err, errs.Unauthenticated) {
		t.Fatalf("err = %v, want Unauthenticated", err)
	}
}

func TestOwnersAreIsolated(t *testing.T) {
	svc := newService(newRepo(), fakeTx{}, &fakeJobs{})
	alice, bob := as(t.Context(), uuid.New()), as(t.Context(), uuid.New())

	n, _ := svc.Create(alice, note.CreateInput{Title: "secret"})
	if _, err := svc.Get(bob, n.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("bob read alice's note: %v", err)
	}
	title := "pwned"
	if _, err := svc.Update(bob, n.ID, note.UpdateInput{Title: &title}); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("bob updated alice's note: %v", err)
	}
	if err := svc.Delete(bob, n.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("bob deleted alice's note: %v", err)
	}
}

func TestUpdateOptimisticConcurrency(t *testing.T) {
	svc := newService(newRepo(), fakeTx{}, &fakeJobs{})
	ctx := as(t.Context(), uuid.New())
	n, _ := svc.Create(ctx, note.CreateInput{Title: "v1"})

	stale := n.Version
	title := "v2"
	updated, err := svc.Update(ctx, n.ID, note.UpdateInput{Title: &title, IfMatch: &stale})
	if err != nil || updated.Version != 2 || updated.Title != "v2" {
		t.Fatalf("update = %+v, %v", updated, err)
	}

	title = "v3 based on stale read"
	if _, err := svc.Update(ctx, n.ID, note.UpdateInput{Title: &title, IfMatch: &stale}); !errors.Is(err, note.ErrVersionMismatch) {
		t.Fatalf("stale If-Match must fail with 412, got %v", err)
	}
}

func TestGetIsCachedAndInvalidatedOnUpdate(t *testing.T) {
	repo := newRepo()
	svc := newService(repo, fakeTx{}, &fakeJobs{})
	ctx := as(t.Context(), uuid.New())
	n, _ := svc.Create(ctx, note.CreateInput{Title: "original"})

	if _, err := svc.Get(ctx, n.ID); err != nil { // warms the cache
		t.Fatal(err)
	}
	title := "changed"
	if _, err := svc.Update(ctx, n.ID, note.UpdateInput{Title: &title}); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(ctx, n.ID)
	if got.Title != "changed" {
		t.Fatalf("stale cache after update: %q", got.Title)
	}
}

func TestListPaginates(t *testing.T) {
	svc := newService(newRepo(), fakeTx{}, &fakeJobs{})
	ctx := as(t.Context(), uuid.New())
	for i := range 5 {
		_, _ = svc.Create(ctx, note.CreateInput{Title: string(rune('a' + i))})
		time.Sleep(time.Millisecond) // distinct created_at
	}

	var seen []string
	req, _ := page.NewRequest(2, "")
	for {
		p, err := svc.List(ctx, note.Filter{}, req)
		if err != nil {
			t.Fatal(err)
		}
		for _, n := range p.Items {
			seen = append(seen, n.Title)
		}
		if p.NextCursor == "" {
			break
		}
		req, _ = page.NewRequest(2, p.NextCursor)
	}
	if !slices.Equal(seen, []string{"e", "d", "c", "b", "a"}) {
		t.Fatalf("pages = %v, want newest first without gaps or duplicates", seen)
	}
}
