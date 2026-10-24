package main

import (
	"log/slog"
	"net/http"
	"net/http/pprof"
	"time"

	"github.com/islamghany/goplate/api"
	"github.com/islamghany/goplate/internal/auth"
	"github.com/islamghany/goplate/internal/config"
	"github.com/islamghany/goplate/internal/lifecycle"
	"github.com/islamghany/goplate/internal/note"
	"github.com/islamghany/goplate/internal/platform/otel"
	"github.com/islamghany/goplate/internal/ratelimit"
	"github.com/islamghany/goplate/internal/user"
	"github.com/islamghany/goplate/internal/web"
)

// modules groups the feature modules mounted on the public router.
type modules struct {
	users *user.Module
	auth  *auth.Module
	notes *note.Module
}

// probePaths are excluded from tracing and logged at debug level.
var probePaths = []string{"/livez", "/readyz"}

// newPublicHandler builds the public API: global middleware around a router
// whose groups carry route-specific middleware.
//
// Request flow (outermost first):
//
//	RequestID → OTel → RealIP → AccessLog → Recover → SecurityHeaders
//	→ CORS → CSRF → MaxBytes → router → [group middleware] → handler
func newPublicHandler(cfg *config.Config, log *slog.Logger, mgr *lifecycle.Manager, limiter ratelimit.Limiter, m modules) (http.Handler, error) {
	router := newRouter(cfg, log, mgr, limiter, m)
	csrf, err := web.CrossOriginProtection(log, cfg.HTTP.TrustedOrigins)
	if err != nil {
		return nil, err
	}
	return web.Chain(router,
		web.RequestIDMiddleware(),
		otel.HTTPMiddleware(probePaths...),
		web.RealIP(cfg.HTTP.TrustedProxies),
		web.AccessLog(log, probePaths...),
		web.Recover(log),
		web.SecurityHeaders(),
		web.CORS(web.CORSConfig{
			AllowedOrigins:   cfg.HTTP.TrustedOrigins,
			AllowedMethods:   []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"},
			AllowedHeaders:   []string{"Authorization", "Content-Type", "If-Match", "X-Request-ID"},
			ExposedHeaders:   []string{"ETag", "Location", "X-Request-ID", "Retry-After", "RateLimit", "RateLimit-Policy"},
			AllowCredentials: cfg.Auth.CookieEnabled,
			MaxAge:           10 * time.Minute,
		}),
		csrf,
		web.MaxBytes(cfg.HTTP.MaxBodyBytes),
	), nil
}

// newRouter mounts every route with its group middleware.
func newRouter(cfg *config.Config, log *slog.Logger, mgr *lifecycle.Manager, limiter ratelimit.Limiter, m modules) *web.Router {
	router := web.NewRouter(log)
	router.HandleHTTP("GET /livez", mgr.LiveHandler())
	router.HandleHTTP("GET /readyz", mgr.ReadyHandler(false))
	if cfg.HTTP.DocsEnabled {
		router.HandleHTTP("GET /openapi.yaml", api.SpecHandler())
		router.HandleHTTP("GET /docs", api.DocsHandler())
	}

	rl := cfg.RateLimit
	global := ratelimit.Policy{Name: "global", Rate: rl.Rate, Period: rl.Period, Burst: rl.Burst}
	login := ratelimit.Policy{Name: "login-ip", Rate: rl.LoginIPRate, Period: time.Minute, Burst: rl.LoginIPRate}

	v1 := router.Group("/v1")
	mountRoutes(m,
		v1.Group("", ratelimit.Middleware(log, limiter, global, ratelimit.ByIP, ratelimit.Options{})),
		v1.Group("", ratelimit.Middleware(log, limiter, login, ratelimit.ByIP, ratelimit.Options{FailClosed: rl.LoginFailClosed})),
		// Authenticate before limiting so authenticated callers get a
		// per-user bucket instead of sharing one per IP (e.g. behind NAT).
		v1.Group("", m.auth.Authenticate(), ratelimit.Middleware(log, limiter, global, ratelimit.ByUserOrIP, ratelimit.Options{})),
	)
	return router
}

// mountRoutes is where each module's routes are attached to a group:
//   - public: anonymous endpoints (IP rate limit)
//   - login: the login endpoint (strict IP rate limit, fail-closed)
//   - protected: endpoints requiring a valid access token
//
// The OpenAPI drift test calls it too, so keep it free of side effects.
func mountRoutes(m modules, public, login, protected *web.Router) {
	m.users.Routes(public, protected)
	m.auth.Routes(public, login, protected)
	m.notes.Routes(protected)
}

// newAdminHandler serves operational endpoints on the internal port:
// metrics, detailed readiness and (optionally) pprof. Never expose it to the
// internet: readiness details and profiles reveal internals.
func newAdminHandler(cfg *config.Config, mgr *lifecycle.Manager, tel *otel.Telemetry) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /metrics", tel.MetricsHandler)
	mux.Handle("GET /livez", mgr.LiveHandler())
	mux.Handle("GET /readyz", mgr.ReadyHandler(true))
	if cfg.Admin.Pprof {
		mux.HandleFunc("/debug/pprof/", pprof.Index)
		mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
		mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
		mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
		mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	}
	return mux
}
