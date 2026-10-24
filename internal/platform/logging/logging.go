// Package logging builds the process-wide *slog.Logger (ADR-0011).
//
// We use plain *slog.Logger everywhere instead of a custom logger type, so
// any library that accepts slog works unchanged. Correlation comes from a
// handler that enriches every record with values from the context:
// trace_id/span_id (OpenTelemetry) and whatever extractors the composition
// root adds (e.g. request_id from the web package). That only works if you
// log with the *Context methods: log.InfoContext(ctx, ...).
package logging

import (
	"context"
	"io"
	"log/slog"
	"path/filepath"

	"go.opentelemetry.io/otel/trace"
)

// Extractor pulls request-scoped attributes out of a context.
type Extractor func(ctx context.Context) []slog.Attr

// Options configure New.
type Options struct {
	Level      slog.Leveler
	Format     string // "json" (default) or "text"
	Service    string
	Version    string
	Extractors []Extractor
}

// New returns a logger writing to w.
func New(w io.Writer, opts Options) *slog.Logger {
	ho := &slog.HandlerOptions{
		Level:     opts.Level,
		AddSource: true,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			// "/home/ci/src/.../note/service.go:42" -> "note/service.go:42"
			if a.Key == slog.SourceKey {
				if src, ok := a.Value.Any().(*slog.Source); ok {
					src.File = filepath.Join(filepath.Base(filepath.Dir(src.File)), filepath.Base(src.File))
				}
			}
			return a
		},
	}
	var h slog.Handler
	if opts.Format == "text" {
		h = slog.NewTextHandler(w, ho)
	} else {
		h = slog.NewJSONHandler(w, ho)
	}
	h = &contextHandler{Handler: h, extractors: opts.Extractors}
	return slog.New(h).With(slog.String("service", opts.Service), slog.String("version", opts.Version))
}

type contextHandler struct {
	slog.Handler
	extractors []Extractor
}

func (h *contextHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	for _, ex := range h.extractors {
		r.AddAttrs(ex(ctx)...)
	}
	return h.Handler.Handle(ctx, r) //nolint:wrapcheck // a decorator must stay transparent
}

func (h *contextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithAttrs(attrs), extractors: h.extractors}
}

func (h *contextHandler) WithGroup(name string) slog.Handler {
	return &contextHandler{Handler: h.Handler.WithGroup(name), extractors: h.extractors}
}
