// Command api runs the goplate HTTP API (and, by default, the background
// job workers).
//
// This package is the composition root (ADR-0005): the ONLY place that knows
// every concrete implementation. It reads config, builds platform adapters,
// hands them to modules as ports, mounts routes and registers everything
// with the lifecycle manager. Read app.go top to bottom to see how the
// whole application is assembled.
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/caarlos0/env/v11"
)

// Set at build time: go build -ldflags "-X main.version=1.2.3 -X main.commit=abc123".
var (
	version = "dev"
	commit  = "none"
)

func main() {
	// SIGTERM (Kubernetes, Docker) and Ctrl-C both trigger graceful shutdown.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Stdout, env.ToMap(os.Environ())); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

// run is main without process-global side effects, so tests can start the
// whole application with their own context, output and environment.
func run(ctx context.Context, stdout io.Writer, environ map[string]string) error {
	app, err := build(ctx, stdout, environ)
	if err != nil {
		return err
	}
	return app.Run(ctx)
}
