//go:build integration

package notepg_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/islamghany/goplate/internal/note"
	"github.com/islamghany/goplate/internal/note/notepg"
	"github.com/islamghany/goplate/internal/page"
	"github.com/islamghany/goplate/internal/platform/postgres"
	"github.com/islamghany/goplate/internal/testutil"
)

var pool *pgxpool.Pool

func TestMain(m *testing.M) {
	env, cleanup, err := testutil.Start(context.Background(), testutil.Options{})
	if err != nil {
		cleanup()
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	pool = env.Pool
	code := m.Run()
	cleanup()
	os.Exit(code)
}

func newNote(owner uuid.UUID, title string, tags ...string) note.Note {
	now := time.Now().UTC().Truncate(time.Microsecond)
	return note.Note{ID: uuid.Must(uuid.NewV7()), OwnerID: owner, Title: title, Tags: tags, Version: 1, CreatedAt: now, UpdatedAt: now}
}

func TestCRUD(t *testing.T) {
	ctx := t.Context()
	repo := notepg.NewRepository(postgres.NewTxManager(pool))
	owner, stranger := testutil.CreateUser(t, pool), testutil.CreateUser(t, pool)

	created, err := repo.Create(ctx, newNote(owner, "first", "a"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := repo.Get(ctx, owner, created.ID)
	if err != nil || got.Title != "first" || !slices.Equal(got.Tags, []string{"a"}) || !got.CreatedAt.Equal(created.CreatedAt) {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	if _, err := repo.Get(ctx, stranger, created.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("other owners must not see the note: %v", err)
	}

	got.Title = "renamed"
	updated, err := repo.Update(ctx, got)
	if err != nil || updated.Version != 2 || updated.Title != "renamed" {
		t.Fatalf("Update = %+v, %v", updated, err)
	}

	if err := repo.Delete(ctx, stranger, created.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("stranger delete: %v", err)
	}
	if err := repo.Delete(ctx, owner, created.ID); err != nil {
		t.Fatal(err)
	}
	if err := repo.Delete(ctx, owner, created.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("second delete: %v", err)
	}
}

func TestListFiltersAndPaginates(t *testing.T) {
	ctx := t.Context()
	repo := notepg.NewRepository(postgres.NewTxManager(pool))
	owner := testutil.CreateUser(t, pool)

	for i, title := range []string{"buy milk", "100% done", "call mom", "milk again", "misc"} {
		n := newNote(owner, title, "t"+fmt.Sprint(i%2))
		n.CreatedAt = n.CreatedAt.Add(time.Duration(i) * time.Second)
		if _, err := repo.Create(ctx, n); err != nil {
			t.Fatal(err)
		}
	}

	list := func(f note.Filter, req page.Request) []string {
		rows, err := repo.List(ctx, owner, f, req)
		if err != nil {
			t.Fatal(err)
		}
		var titles []string
		for _, r := range rows {
			titles = append(titles, r.Title)
		}
		return titles
	}

	if got := list(note.Filter{Search: "MILK"}, page.Request{Limit: 10}); !slices.Equal(got, []string{"milk again", "buy milk"}) {
		t.Errorf("search = %v", got)
	}
	if got := list(note.Filter{Search: "%"}, page.Request{Limit: 10}); !slices.Equal(got, []string{"100% done"}) {
		t.Errorf("%% must be matched literally, got %v", got)
	}
	if got := list(note.Filter{Tag: "t1"}, page.Request{Limit: 10}); !slices.Equal(got, []string{"milk again", "100% done"}) {
		t.Errorf("tag = %v", got)
	}

	// Newest first: misc, milk again, call mom, 100% done, buy milk.
	firstPage, _ := repo.List(ctx, owner, note.Filter{}, page.Request{Limit: 2})
	if len(firstPage) != 3 { // limit+1 tells the service another page exists
		t.Fatalf("want limit+1 rows, got %d", len(firstPage))
	}
	after := &page.Cursor{CreatedAt: firstPage[1].CreatedAt, ID: firstPage[1].ID}
	if got := list(note.Filter{}, page.Request{Limit: 10, After: after}); !slices.Equal(got, []string{"call mom", "100% done", "buy milk"}) {
		t.Errorf("after cursor = %v", got)
	}
}

func TestWithinTxRollsBack(t *testing.T) {
	ctx := t.Context()
	txm := postgres.NewTxManager(pool)
	repo := notepg.NewRepository(txm)
	owner := testutil.CreateUser(t, pool)
	n := newNote(owner, "never committed")

	errBoom := errors.New("boom")
	err := txm.WithinTx(ctx, func(ctx context.Context) error {
		if _, err := repo.Create(ctx, n); err != nil {
			return err
		}
		if _, err := repo.Get(ctx, owner, n.ID); err != nil { // visible inside the tx
			return err
		}
		return errBoom
	})
	if !errors.Is(err, errBoom) {
		t.Fatalf("err = %v", err)
	}
	if _, err := repo.Get(ctx, owner, n.ID); !errors.Is(err, note.ErrNotFound) {
		t.Fatalf("insert must be rolled back, got %v", err)
	}
}
