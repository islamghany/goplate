// Command admin runs one-off operational tasks against the database:
//
//	admin migrate up       apply all pending migrations (ours + River's)
//	admin migrate down     roll back the latest application migration
//	admin migrate status   list migrations and whether they are applied
//	admin migrate create <name>   scaffold migrations/NNNNN_<name>.sql
//	admin seed             create a development admin user (idempotent)
//
// Deploys run `admin migrate up` as a separate step before rolling out the
// API (ADR-0019). Any failure exits non-zero so the deploy stops.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/pressly/goose/v3"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/platform/migrate"
	"github.com/islamghany/goplate/internal/platform/password"
	"github.com/islamghany/goplate/internal/platform/postgres"
	"github.com/islamghany/goplate/internal/user"
	"github.com/islamghany/goplate/internal/user/userpg"
)

const usage = `usage: admin <command>

commands:
  migrate up|down|status   manage the database schema
  migrate create <name>    create a new SQL migration in ./migrations
  seed                     create the development admin user (needs SEED_ADMIN_EMAIL, SEED_ADMIN_PASSWORD)`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], env.ToMap(os.Environ()), os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, environ map[string]string, stdout io.Writer) error {
	if len(args) == 0 {
		return errors.New(usage)
	}
	log := slog.New(slog.NewTextHandler(stdout, nil))

	// Creating a migration needs no database.
	if len(args) == 3 && args[0] == "migrate" && args[1] == "create" {
		goose.SetSequential(true)
		return goose.Create(nil, "migrations", args[2], "sql")
	}

	// The admin tool only needs the database, so it reads just that
	// variable instead of the API's full configuration.
	dsn := environ["POSTGRES_URL"]
	if dsn == "" {
		return errors.New("POSTGRES_URL is required")
	}
	pool, err := postgres.Open(ctx, postgres.Config{URL: dsn, MaxConns: 4})
	if err != nil {
		return err
	}
	defer pool.Close()

	switch {
	case len(args) == 2 && args[0] == "migrate" && args[1] == "up":
		return migrate.Up(ctx, pool, log)
	case len(args) == 2 && args[0] == "migrate" && args[1] == "down":
		return migrate.Down(ctx, pool, log)
	case len(args) == 2 && args[0] == "migrate" && args[1] == "status":
		return migrate.Status(ctx, pool, log)
	case len(args) == 1 && args[0] == "seed":
		return seed(ctx, pool, environ, log)
	default:
		return errors.New(usage)
	}
}

// seed creates an admin account for local development. Running it twice is
// harmless: an existing account is left untouched.
func seed(ctx context.Context, pool *pgxpool.Pool, environ map[string]string, log *slog.Logger) error {
	email, pw := environ["SEED_ADMIN_EMAIL"], environ["SEED_ADMIN_PASSWORD"]
	if email == "" || len(pw) < 12 {
		return errors.New("SEED_ADMIN_EMAIL and SEED_ADMIN_PASSWORD (12+ characters) are required")
	}
	repo := userpg.NewRepository(postgres.NewTxManager(pool))
	hasher := password.NewHasher(password.DefaultParams)

	svc := user.NewService(repo, hasher)
	u, err := svc.Register(ctx, user.RegisterInput{Email: email, Name: "Admin", Password: pw})
	if errs.Is(err, errs.Conflict) {
		log.InfoContext(ctx, "seed user already exists; skipping", "email", email)
		return nil
	}
	if err != nil {
		return err
	}
	// The API deliberately has no "make admin" endpoint; promotion is an
	// operator action, so it is plain SQL here.
	if _, err := pool.Exec(ctx, `UPDATE users SET role = $1 WHERE id = $2`, identity.RoleAdmin, u.ID); err != nil {
		return fmt.Errorf("promote seed user: %w", err)
	}
	log.InfoContext(ctx, "seeded admin user", "email", email, "role", identity.RoleAdmin)
	return nil
}
