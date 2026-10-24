//go:build integration

package riverjobs_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/islamghany/goplate/internal/platform/postgres"
	"github.com/islamghany/goplate/internal/platform/riverjobs"
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

type ping struct{ N int }

func (ping) Kind() string { return "test.ping" }

func countJobs(t *testing.T) int {
	t.Helper()
	var n int
	if err := pool.QueryRow(t.Context(), `SELECT count(*) FROM river_job WHERE kind = 'test.ping'`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// The reason River was chosen (ADR-0010): a job enqueued inside a
// transaction exists if and only if the transaction commits.
func TestEnqueueJoinsTransaction(t *testing.T) {
	ctx := t.Context()
	q := riverjobs.New(pool, riverjobs.Config{}, slog.New(slog.DiscardHandler))
	riverjobs.Register(q, func(context.Context, ping) error { return nil })
	if err := q.Build(); err != nil {
		t.Fatal(err)
	}
	txm := postgres.NewTxManager(pool)
	before := countJobs(t)

	errRollback := errors.New("rollback")
	_ = txm.WithinTx(ctx, func(ctx context.Context) error {
		if err := q.Enqueue(ctx, ping{1}); err != nil {
			t.Fatal(err)
		}
		return errRollback
	})
	if got := countJobs(t); got != before {
		t.Fatalf("rolled-back job was persisted: %d -> %d", before, got)
	}

	if err := txm.WithinTx(ctx, func(ctx context.Context) error { return q.Enqueue(ctx, ping{2}) }); err != nil {
		t.Fatal(err)
	}
	if got := countJobs(t); got != before+1 {
		t.Fatalf("committed job missing: %d -> %d", before, got)
	}
}

func TestWorkersProcessJobs(t *testing.T) {
	ctx := t.Context()
	got := make(chan int, 1)
	q := riverjobs.New(pool, riverjobs.Config{WorkerEnabled: true, MaxWorkers: 2}, slog.New(slog.DiscardHandler))
	riverjobs.Register(q, func(_ context.Context, p ping) error { got <- p.N; return nil })
	if err := q.Build(); err != nil {
		t.Fatal(err)
	}
	if err := q.Start(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = q.Stop(context.Background()) })

	if err := q.Enqueue(ctx, ping{42}); err != nil {
		t.Fatal(err)
	}
	select {
	case n := <-got:
		if n != 42 && n != 2 { // a job committed by the other test may run first
			t.Fatalf("worker got %d", n)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("job was not processed")
	}
}
