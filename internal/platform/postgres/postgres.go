// Package postgres provides the Postgres connection pool and the
// transaction manager shared by every module's repository adapter (ADR-0004).
//
// Repository adapters never hold the pool directly. They hold a *TxManager
// and call DB(ctx) for every query, which returns the transaction carried by
// ctx when one is open, or the pool otherwise. This is what makes
//
//	txm.WithinTx(ctx, func(ctx context.Context) error {
//		repoA.Create(ctx, ...)   // same transaction
//		jobs.Enqueue(ctx, ...)   // same transaction (River uses it too)
//		return nil
//	})
//
// atomic without services ever importing pgx.
package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Config holds pool settings.
type Config struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
}

// Open creates a pool, verifies connectivity and instruments it with
// OpenTelemetry tracing (one span per query) and pool metrics.
func Open(ctx context.Context, cfg Config) (*pgxpool.Pool, error) {
	pc, err := pgxpool.ParseConfig(cfg.URL)
	if err != nil {
		return nil, fmt.Errorf("parse postgres url: %w", err)
	}
	if cfg.MaxConns > 0 {
		pc.MaxConns = cfg.MaxConns
	}
	pc.MinConns = cfg.MinConns
	if cfg.MaxConnLifetime > 0 {
		pc.MaxConnLifetime = cfg.MaxConnLifetime
	}
	if cfg.MaxConnIdleTime > 0 {
		pc.MaxConnIdleTime = cfg.MaxConnIdleTime
	}
	pc.ConnConfig.Tracer = otelpgx.NewTracer()

	pool, err := pgxpool.NewWithConfig(ctx, pc)
	if err != nil {
		return nil, fmt.Errorf("create postgres pool: %w", err)
	}

	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("ping postgres: %w", err)
	}
	if err := otelpgx.RecordStats(pool); err != nil {
		pool.Close()
		return nil, fmt.Errorf("record pool stats: %w", err)
	}
	return pool, nil
}

// DBTX is the query interface shared by *pgxpool.Pool and pgx.Tx. It matches
// the interface sqlc generates, so generated code accepts it directly.
type DBTX interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row
}

type txKey struct{}

// TxManager runs functions inside transactions carried by context.
type TxManager struct {
	pool *pgxpool.Pool
}

// NewTxManager returns a TxManager over pool.
func NewTxManager(pool *pgxpool.Pool) *TxManager {
	return &TxManager{pool: pool}
}

// WithinTx runs fn in a transaction. The transaction is committed if fn
// returns nil and rolled back otherwise (including on panic). Calls nested
// inside fn join the outer transaction instead of opening a new one.
func (m *TxManager) WithinTx(ctx context.Context, fn func(ctx context.Context) error) error {
	if _, ok := TxFromContext(ctx); ok {
		return fn(ctx)
	}
	return pgx.BeginTxFunc(ctx, m.pool, pgx.TxOptions{}, func(tx pgx.Tx) error {
		return fn(context.WithValue(ctx, txKey{}, tx))
	})
}

// DB returns the transaction in ctx, or the pool when there is none.
func (m *TxManager) DB(ctx context.Context) DBTX {
	if tx, ok := TxFromContext(ctx); ok {
		return tx
	}
	return m.pool
}

// TxFromContext returns the transaction opened by WithinTx, if any. Other
// adapters (the job queue) use it to join the caller's transaction.
func TxFromContext(ctx context.Context) (pgx.Tx, bool) {
	tx, ok := ctx.Value(txKey{}).(pgx.Tx)
	return tx, ok
}

// IsNoRows reports whether err means "query returned no rows".
func IsNoRows(err error) bool { return errors.Is(err, pgx.ErrNoRows) }

// UniqueViolation returns the violated constraint name when err is a unique
// violation (SQLSTATE 23505). Adapters translate it into a domain Conflict.
func UniqueViolation(err error) (constraint string, ok bool) {
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) && pgErr.Code == "23505" {
		return pgErr.ConstraintName, true
	}
	return "", false
}
