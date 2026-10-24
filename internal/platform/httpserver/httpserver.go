// Package httpserver wraps http.Server as a lifecycle component with safe
// timeouts (ADR-0012).
//
// A bare http.Server has no timeouts at all, which lets slow clients hold
// connections open indefinitely (slowloris). Every server goplate runs goes
// through this package so that mistake cannot happen.
package httpserver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"time"
)

// Timeouts bound each phase of a connection.
type Timeouts struct {
	ReadHeader time.Duration
	Read       time.Duration
	Write      time.Duration
	Idle       time.Duration
}

// Server is an HTTP server component.
type Server struct {
	name   string
	addr   string
	srv    *http.Server
	ln     net.Listener
	faults chan error
}

// New returns a Server named name that will listen on addr.
func New(name, addr string, h http.Handler, t Timeouts, log *slog.Logger) *Server {
	return &Server{
		name: name,
		addr: addr,
		srv: &http.Server{
			Handler:           h,
			ReadHeaderTimeout: t.ReadHeader,
			ReadTimeout:       t.Read,
			WriteTimeout:      t.Write,
			IdleTimeout:       t.Idle,
			MaxHeaderBytes:    1 << 20,
			ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
		},
		faults: make(chan error, 1),
	}
}

func (s *Server) Name() string { return s.name }

// Start binds the port synchronously, so "address already in use" fails
// startup immediately, then serves in the background.
func (s *Server) Start(ctx context.Context) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", s.addr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", s.addr, err)
	}
	s.ln = ln
	go func() {
		if err := s.srv.Serve(ln); !errors.Is(err, http.ErrServerClosed) {
			s.faults <- err
		}
		close(s.faults)
	}()
	return nil
}

// Stop stops accepting connections and waits for in-flight requests. If
// the deadline expires, remaining connections are closed forcibly.
func (s *Server) Stop(ctx context.Context) error {
	if err := s.srv.Shutdown(ctx); err != nil {
		_ = s.srv.Close()
		return fmt.Errorf("shutdown %s: %w", s.name, err)
	}
	return nil
}

// Faults implements lifecycle.Faulter.
func (s *Server) Faults() <-chan error { return s.faults }

// Addr returns the bound address (useful when addr was ":0" in tests).
func (s *Server) Addr() string {
	if s.ln == nil {
		return s.addr
	}
	return s.ln.Addr().String()
}
