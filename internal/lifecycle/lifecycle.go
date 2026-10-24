// Package lifecycle starts, health-checks and gracefully stops the
// long-running parts of the process (ADR-0012).
//
// Everything that owns a resource or a goroutine — the HTTP servers, the job
// workers, connection pools, telemetry exporters — is a Component. The
// Manager starts components in registration order and stops them in
// reverse, so dependencies (pools) outlive their users (servers, workers):
//
//	register:  postgres → redis → telemetry → jobs → admin http → public http
//	shutdown:  public http → admin http → jobs → telemetry → redis → postgres
//
// Shutdown sequence on SIGTERM (or when any component fails):
//
//  1. readiness flips to "not ready" so load balancers stop routing to us;
//  2. wait DrainDelay so they notice;
//  3. stop components in reverse order, all within ShutdownTimeout.
package lifecycle

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync/atomic"
	"time"
)

// Component is anything with a start/stop lifecycle.
type Component interface {
	// Name identifies the component in logs and health reports.
	Name() string
	// Start acquires resources and launches background work, then returns.
	// It must not block for the component's lifetime. An error aborts startup.
	Start(ctx context.Context) error
	// Stop releases resources gracefully, giving up when ctx expires.
	Stop(ctx context.Context) error
}

// Faulter is implemented by components whose background work can fail after
// Start returned (e.g. an HTTP server's Serve loop). A value received on the
// channel is fatal: the Manager shuts the whole process down.
type Faulter interface {
	Faults() <-chan error
}

// Checker is implemented by dependencies that can report readiness.
type Checker interface {
	Name() string
	Check(ctx context.Context) error
}

// Config tunes shutdown behavior.
type Config struct {
	DrainDelay      time.Duration // wait after flipping readiness, before stopping
	ShutdownTimeout time.Duration // upper bound for stopping everything
}

// Manager orchestrates components. Create it with New, register with Add
// and AddChecker, then call Run once.
type Manager struct {
	log        *slog.Logger
	cfg        Config
	components []Component
	checkers   []Checker
	ready      atomic.Bool
	health     *health
}

// New returns a Manager.
func New(log *slog.Logger, cfg Config) *Manager {
	m := &Manager{log: log, cfg: cfg}
	m.health = newHealth(m)
	return m
}

// Add registers components. Order matters: see the package documentation.
func (m *Manager) Add(cs ...Component) { m.components = append(m.components, cs...) }

// AddChecker registers readiness checks.
func (m *Manager) AddChecker(cs ...Checker) { m.checkers = append(m.checkers, cs...) }

// Ready reports whether the process is accepting traffic.
func (m *Manager) Ready() bool { return m.ready.Load() }

// Run starts every component, blocks until ctx is canceled (a signal) or a
// component faults, then shuts everything down. It returns the fault (if
// any) joined with shutdown errors; a clean signal-triggered shutdown
// returns nil.
func (m *Manager) Run(ctx context.Context) error {
	faults := make(chan error, len(m.components))
	var started []Component

	for _, c := range m.components {
		m.log.InfoContext(ctx, "starting component", "component", c.Name())
		if err := c.Start(ctx); err != nil {
			startErr := fmt.Errorf("start %s: %w", c.Name(), err)
			return errors.Join(startErr, m.stop(ctx, started))
		}
		started = append(started, c)
		if f, ok := c.(Faulter); ok {
			go func() {
				if err, ok := <-f.Faults(); ok && err != nil {
					faults <- fmt.Errorf("%s: %w", c.Name(), err)
				}
			}()
		}
	}

	m.ready.Store(true)
	m.log.InfoContext(ctx, "all components started; ready")

	var fault error
	select {
	case <-ctx.Done():
		m.log.InfoContext(ctx, "shutdown requested", "cause", context.Cause(ctx))
	case fault = <-faults:
		m.log.ErrorContext(ctx, "component failed; shutting down", "err", fault)
	}

	m.ready.Store(false)
	if fault == nil && m.cfg.DrainDelay > 0 {
		m.log.InfoContext(ctx, "draining before shutdown", "delay", m.cfg.DrainDelay)
		time.Sleep(m.cfg.DrainDelay)
	}
	return errors.Join(fault, m.stop(ctx, started))
}

// stop stops components in reverse order within the shutdown budget.
func (m *Manager) stop(ctx context.Context, started []Component) error {
	// The parent ctx is already canceled on shutdown; keep its values (for
	// logging/tracing) but give stopping its own deadline.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.ShutdownTimeout)
	defer cancel()

	var errs []error
	for _, c := range slices.Backward(started) {
		begin := time.Now()
		if err := c.Stop(ctx); err != nil {
			m.log.ErrorContext(ctx, "component stop failed", "component", c.Name(), "err", err)
			errs = append(errs, fmt.Errorf("stop %s: %w", c.Name(), err))
			continue
		}
		m.log.InfoContext(ctx, "component stopped", "component", c.Name(), "took", time.Since(begin))
	}
	return errors.Join(errs...)
}

// Func adapts plain functions into a Component. Use it for resources that
// only need closing, e.g. a connection pool:
//
//	lifecycle.Func("postgres", nil, func(context.Context) error { pool.Close(); return nil })
func Func(name string, start, stop func(context.Context) error) Component {
	return funcComponent{name: name, start: start, stop: stop}
}

type funcComponent struct {
	name        string
	start, stop func(context.Context) error
}

func (f funcComponent) Name() string { return f.name }

func (f funcComponent) Start(ctx context.Context) error {
	if f.start == nil {
		return nil
	}
	return f.start(ctx)
}

func (f funcComponent) Stop(ctx context.Context) error {
	if f.stop == nil {
		return nil
	}
	return f.stop(ctx)
}

// CheckFunc adapts a function into a Checker.
func CheckFunc(name string, check func(context.Context) error) Checker {
	return checkFunc{name: name, check: check}
}

type checkFunc struct {
	name  string
	check func(context.Context) error
}

func (c checkFunc) Name() string                    { return c.name }
func (c checkFunc) Check(ctx context.Context) error { return c.check(ctx) }
