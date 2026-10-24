//go:build integration

// Package testutil starts real infrastructure for integration tests
// (ADR-0017): Postgres and Redis in throwaway containers, with migrations
// applied. It only compiles with the "integration" build tag:
//
//	make test-integration     # go test -tags=integration ./...
//
// Start containers once per package from TestMain, and keep tests
// independent by giving each its own data (e.g. a fresh user per test)
// rather than truncating shared tables.
package testutil

import (
	"context"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	tcredis "github.com/testcontainers/testcontainers-go/modules/redis"

	"github.com/islamghany/goplate/internal/platform/migrate"
	"github.com/islamghany/goplate/internal/platform/postgres"
)

// Env is a running set of dependencies.
type Env struct {
	PostgresURL string
	RedisURL    string // empty unless requested
	Pool        *pgxpool.Pool
}

// Options select what to start.
type Options struct {
	Redis bool
}

// Start launches the containers, migrates the database and returns a
// cleanup function that stops everything.
func Start(ctx context.Context, opts Options) (*Env, func(), error) {
	var cleanups []func()
	cleanup := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	pg, err := tcpostgres.Run(ctx, "postgres:17-alpine",
		tcpostgres.WithDatabase("goplate"),
		tcpostgres.WithUsername("goplate"),
		tcpostgres.WithPassword("goplate"),
		tcpostgres.BasicWaitStrategies(),
	)
	if err != nil {
		return nil, cleanup, fmt.Errorf("start postgres: %w", err)
	}
	cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(pg) })

	env := &Env{}
	if env.PostgresURL, err = pg.ConnectionString(ctx, "sslmode=disable"); err != nil {
		return nil, cleanup, fmt.Errorf("postgres url: %w", err)
	}
	if env.Pool, err = postgres.Open(ctx, postgres.Config{URL: env.PostgresURL, MaxConns: 10}); err != nil {
		return nil, cleanup, err
	}
	cleanups = append(cleanups, env.Pool.Close)

	if err := migrate.Up(ctx, env.Pool, slog.New(slog.DiscardHandler)); err != nil {
		return nil, cleanup, err
	}

	if opts.Redis {
		rc, err := tcredis.Run(ctx, "redis:7-alpine")
		if err != nil {
			return nil, cleanup, fmt.Errorf("start redis: %w", err)
		}
		cleanups = append(cleanups, func() { _ = testcontainers.TerminateContainer(rc) })
		if env.RedisURL, err = rc.ConnectionString(ctx); err != nil {
			return nil, cleanup, fmt.Errorf("redis url: %w", err)
		}
	}
	return env, cleanup, nil
}

// CreateUser inserts a user directly and returns its ID. Tests that need an
// owner for their rows call it so they never share data with other tests.
func CreateUser(t *testing.T, pool *pgxpool.Pool) uuid.UUID {
	t.Helper()
	id := uuid.Must(uuid.NewV7())
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, err := pool.Exec(t.Context(),
		`INSERT INTO users (id, email, name, password_hash, role, created_at, updated_at)
		 VALUES ($1, $2, 'Test', 'x', 'user', $3, $3)`,
		id, id.String()+"@test.local", now)
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return id
}
