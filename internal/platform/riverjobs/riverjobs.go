// Package riverjobs adapts River (a Postgres-backed job queue) to goplate's
// jobs vocabulary (ADR-0010).
//
// The key property: Enqueue joins the transaction carried by ctx (see
// platform/postgres.TxManager), so a job is committed or rolled back together
// with the business data that caused it. No lost jobs, no phantom jobs, and
// no outbox table to maintain.
//
// The client is built in two phases because modules need an enqueuer before
// their handlers exist, while River fixes its worker set at construction:
//
//	q := riverjobs.New(pool, cfg, log)                 // 1. modules get q as their JobEnqueuer
//	notes := note.NewModule(note.Deps{Jobs: q, ...})
//	riverjobs.Register(q, notes.Service.HandleCreated) // 2. register handlers
//	q.Every(time.Hour, auth.PurgeExpiredSessions{})
//	err := q.Build()                                   // 3. freeze and create the River client
package riverjobs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"
	"github.com/riverqueue/river/riverdriver/riverpgxv5"
	"github.com/riverqueue/river/rivertype"
	"github.com/riverqueue/rivercontrib/otelriver"

	"github.com/islamghany/goplate/internal/jobs"
	"github.com/islamghany/goplate/internal/platform/postgres"
)

// Config controls the client.
type Config struct {
	// WorkerEnabled makes this process work jobs. When false the client is
	// insert-only, so one binary can run as API-only, worker-only or both.
	WorkerEnabled bool
	MaxWorkers    int
}

// Client is the job queue: it implements the modules' JobEnqueuer ports and
// is a lifecycle component.
type Client struct {
	pool     *pgxpool.Pool
	cfg      Config
	log      *slog.Logger
	workers  *river.Workers
	periodic []*river.PeriodicJob
	river    *river.Client[pgx.Tx]
}

// New returns an unbuilt client. Register handlers, then call Build.
func New(pool *pgxpool.Pool, cfg Config, log *slog.Logger) *Client {
	return &Client{pool: pool, cfg: cfg, log: log, workers: river.NewWorkers()}
}

// Register adds the handler for jobs of type T. Modules expose plain
// handler functions; only the composition root calls Register, so modules
// never import River.
func Register[T jobs.Args](c *Client, h jobs.Handler[T]) {
	river.AddWorker(c.workers, river.WorkFunc(func(ctx context.Context, j *river.Job[T]) error {
		return h(ctx, j.Args)
	}))
}

// Every enqueues args at a fixed interval (and once at start). River's
// leader election ensures only one instance enqueues it.
func (c *Client) Every(interval time.Duration, args jobs.Args) {
	c.periodic = append(c.periodic, river.NewPeriodicJob(
		river.PeriodicInterval(interval),
		func() (river.JobArgs, *river.InsertOpts) { return args, nil },
		&river.PeriodicJobOpts{RunOnStart: true},
	))
}

// Build creates the underlying River client. Call it once, after every
// Register/Every and before Start or Enqueue.
func (c *Client) Build() error {
	rc := &river.Config{
		Logger:       c.log,
		Workers:      c.workers,
		PeriodicJobs: c.periodic,
		JobTimeout:   time.Minute,
		// Spans and metrics for every insert and job run (ADR-0011).
		Middleware: []rivertype.Middleware{otelriver.NewMiddleware(nil)},
	}
	if c.cfg.WorkerEnabled {
		rc.Queues = map[string]river.QueueConfig{
			river.QueueDefault: {MaxWorkers: max(c.cfg.MaxWorkers, 1)},
		}
	}
	rc2, err := river.NewClient(riverpgxv5.New(c.pool), rc)
	if err != nil {
		return fmt.Errorf("create river client: %w", err)
	}
	c.river = rc2
	return nil
}

var errNotBuilt = errors.New("riverjobs: Build was not called")

// Enqueue inserts a job, inside the caller's transaction when ctx has one.
func (c *Client) Enqueue(ctx context.Context, args jobs.Args) error {
	if c.river == nil {
		return errNotBuilt
	}
	var err error
	if tx, ok := postgres.TxFromContext(ctx); ok {
		_, err = c.river.InsertTx(ctx, tx, args, nil)
	} else {
		_, err = c.river.Insert(ctx, args, nil)
	}
	if err != nil {
		return fmt.Errorf("enqueue %s: %w", args.Kind(), err)
	}
	return nil
}

// Name implements lifecycle.Component.
func (c *Client) Name() string { return "jobs" }

// Start begins working jobs when workers are enabled.
func (c *Client) Start(ctx context.Context) error {
	if c.river == nil {
		return errNotBuilt
	}
	if !c.cfg.WorkerEnabled {
		return nil
	}
	// The fetch loops must outlive the startup context; they end via Stop.
	if err := c.river.Start(context.WithoutCancel(ctx)); err != nil {
		return fmt.Errorf("start river: %w", err)
	}
	return nil
}

// Stop stops fetching new jobs and waits for running ones to finish. If the
// shutdown deadline hits first, remaining jobs are canceled; River retries
// them later because delivery is at-least-once.
func (c *Client) Stop(ctx context.Context) error {
	if c.river == nil || !c.cfg.WorkerEnabled {
		return nil
	}
	err := c.river.Stop(ctx)
	if errors.Is(err, context.DeadlineExceeded) {
		hardCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer cancel()
		if err := c.river.StopAndCancel(hardCtx); err != nil {
			return fmt.Errorf("stop river (cancel): %w", err)
		}
		return nil
	}
	if err != nil {
		return fmt.Errorf("stop river: %w", err)
	}
	return nil
}
