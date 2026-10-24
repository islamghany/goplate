// Package otel sets up OpenTelemetry tracing and metrics (ADR-0011).
//
// Application code only uses the vendor-neutral OTel API; this package picks
// the exporters:
//
//   - traces: OTLP over HTTP, configured by the standard OTEL_EXPORTER_OTLP_*
//     environment variables (point it at Jaeger, Tempo, Honeycomb, ...).
//     OTEL_TRACES_EXPORTER=none turns tracing off.
//   - metrics: a Prometheus registry served on the internal admin port.
//
// The providers are installed globally (otel.SetTracerProvider), which is
// how OTel instrumentation libraries (otelhttp, otelpgx, redisotel) find them.
package otel

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strings"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	otelprom "go.opentelemetry.io/otel/exporters/prometheus"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/islamghany/goplate/internal/web"
)

// Config identifies the service in telemetry.
type Config struct {
	ServiceName    string
	ServiceVersion string
	Environment    string
}

// Telemetry holds the configured providers.
type Telemetry struct {
	// MetricsHandler serves Prometheus metrics; mount it on the admin port.
	MetricsHandler http.Handler
	shutdown       []func(context.Context) error
}

// Setup installs global tracer/meter providers and propagators.
func Setup(ctx context.Context, cfg Config, log *slog.Logger) (*Telemetry, error) {
	res, err := resource.Merge(resource.Default(), resource.NewWithAttributes(semconv.SchemaURL,
		semconv.ServiceName(cfg.ServiceName),
		semconv.ServiceVersion(cfg.ServiceVersion),
		attribute.String("deployment.environment.name", cfg.Environment),
	))
	if err != nil {
		return nil, fmt.Errorf("otel resource: %w", err)
	}

	t := &Telemetry{}
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		log.WarnContext(ctx, "opentelemetry error", "err", err)
	}))
	// W3C trace context + baggage: continue traces started upstream.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	if os.Getenv("OTEL_TRACES_EXPORTER") != "none" {
		exp, err := otlptracehttp.New(ctx)
		if err != nil {
			return nil, fmt.Errorf("otlp trace exporter: %w", err)
		}
		// The sampler honours OTEL_TRACES_SAMPLER / OTEL_TRACES_SAMPLER_ARG.
		tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exp), sdktrace.WithResource(res))
		otel.SetTracerProvider(tp)
		t.shutdown = append(t.shutdown, tp.Shutdown)
	}

	reg := prometheus.NewRegistry()
	reg.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
	)
	promExp, err := otelprom.New(otelprom.WithRegisterer(reg))
	if err != nil {
		return nil, fmt.Errorf("prometheus exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(promExp), sdkmetric.WithResource(res))
	otel.SetMeterProvider(mp)
	t.shutdown = append(t.shutdown, mp.Shutdown)
	t.MetricsHandler = promhttp.HandlerFor(reg, promhttp.HandlerOpts{})

	return t, nil
}

// Name implements lifecycle.Component.
func (t *Telemetry) Name() string { return "telemetry" }

// Start implements lifecycle.Component (providers are live after Setup).
func (t *Telemetry) Start(context.Context) error { return nil }

// Stop flushes pending spans and metrics.
func (t *Telemetry) Stop(ctx context.Context) error {
	var errs []error
	for _, fn := range t.shutdown {
		errs = append(errs, fn(ctx))
	}
	return errors.Join(errs...)
}

// HTTPMiddleware traces and measures every request. Span names and the
// http.route metric label use the matched route pattern (set by the web
// router), never the raw path, which would explode cardinality.
func HTTPMiddleware(skipPaths ...string) web.Middleware {
	return func(next http.Handler) http.Handler {
		tagged := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			next.ServeHTTP(w, r)
			route := web.RoutePattern(r.Context()) // "GET /v1/notes/{id}"
			if route == "" {
				return
			}
			// Semantic conventions: span name "{method} {route}", and the
			// http.route attribute is the path template alone.
			_, path, _ := strings.Cut(route, " ")
			span := trace.SpanFromContext(r.Context())
			span.SetName(route)
			span.SetAttributes(semconv.HTTPRoute(path))
			if l, ok := otelhttp.LabelerFromContext(r.Context()); ok {
				l.Add(semconv.HTTPRoute(path))
			}
		})
		return otelhttp.NewHandler(tagged, "http.request",
			otelhttp.WithFilter(func(r *http.Request) bool {
				for _, p := range skipPaths {
					if r.URL.Path == p {
						return false
					}
				}
				return true
			}),
		)
	}
}
