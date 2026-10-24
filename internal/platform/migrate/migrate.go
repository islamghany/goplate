// Package migrate applies schema migrations: ours (goose, embedded SQL) and
// River's (its job tables), in one step (ADR-0019).
//
// A Postgres advisory lock serializes concurrent runs, so several replicas
// or an overlapping deploy can never migrate at the same time.
package migrate

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"
	"github.com/pressly/goose/v3/lock"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivermigrate"

	"github.com/islamghany/goplate/migrations"
)

// Up applies every pending migration.
func Up(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	// River first: our migrations never depend on River's tables, and
	// workers need them before the API starts enqueuing.
	rm, err := rivermigrate.New(riverpgxv5.New(pool), &rivermigrate.Config{Logger: log})
	if err != nil {
		return fmt.Errorf("river migrator: %w", err)
	}
	res, err := rm.Migrate(ctx, rivermigrate.DirectionUp, nil)
	if err != nil {
		return fmt.Errorf("river migrations: %w", err)
	}
	for _, v := range res.Versions {
		log.InfoContext(ctx, "applied river migration", "version", v.Version)
	}

	p, closeFn, err := provider(pool)
	if err != nil {
		return err
	}
	defer closeFn()
	results, err := p.Up(ctx)
	for _, r := range results {
		log.InfoContext(ctx, "applied migration", "version", r.Source.Version, "file", r.Source.Path, "took", r.Duration)
	}
	if err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}
	return nil
}

// Down rolls back the most recent application migration (not River's).
func Down(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	p, closeFn, err := provider(pool)
	if err != nil {
		return err
	}
	defer closeFn()
	r, err := p.Down(ctx)
	if err != nil {
		return fmt.Errorf("roll back migration: %w", err)
	}
	log.InfoContext(ctx, "rolled back migration", "version", r.Source.Version, "file", r.Source.Path)
	return nil
}

// Status logs the state of every application migration.
func Status(ctx context.Context, pool *pgxpool.Pool, log *slog.Logger) error {
	p, closeFn, err := provider(pool)
	if err != nil {
		return err
	}
	defer closeFn()
	statuses, err := p.Status(ctx)
	if err != nil {
		return fmt.Errorf("migration status: %w", err)
	}
	for _, s := range statuses {
		log.InfoContext(ctx, "migration", "version", s.Source.Version, "file", s.Source.Path, "state", s.State, "applied_at", s.AppliedAt)
	}
	return nil
}

func provider(pool *pgxpool.Pool) (*goose.Provider, func(), error) {
	locker, err := lock.NewPostgresSessionLocker()
	if err != nil {
		return nil, nil, fmt.Errorf("migration lock: %w", err)
	}
	db := stdlib.OpenDBFromPool(pool) // goose speaks database/sql; reuse the pgx pool
	p, err := goose.NewProvider(goose.DialectPostgres, db, migrations.FS, goose.WithSessionLocker(locker))
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("migration provider: %w", err)
	}
	return p, func() { _ = db.Close() }, nil
}
