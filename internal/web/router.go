// Package web is goplate's thin HTTP toolkit on top of net/http (ADR-0003).
//
// It adds exactly what the standard library lacks for an API server:
//
//   - HandlerFunc: handlers return an error; one central encoder turns it
//     into an RFC 9457 problem response (ADR-0018), so no handler can forget
//     to log, sanitize or format an error.
//   - Router: route groups with per-group middleware and a registry of every
//     route (used by the OpenAPI drift test), on top of http.ServeMux.
//   - Request helpers: strict JSON decoding, path/query parsing, responses.
//   - Standard middleware: request IDs, access logs, panic recovery, CORS...
//
// Middleware is plain func(http.Handler) http.Handler so any third-party
// net/http middleware plugs in unchanged. Handlers never see ServeMux; to
// switch routers, change this file only.
package web

import (
	"log/slog"
	"net/http"
	"slices"
	"strings"
)

// HandlerFunc is an HTTP handler that may fail. Returning an error (instead
// of writing one) lets the router render every failure the same way.
type HandlerFunc func(w http.ResponseWriter, r *http.Request) error

// Middleware is the standard net/http middleware shape.
type Middleware = func(http.Handler) http.Handler

// Chain wraps h with mw so that mw[0] is the outermost layer.
func Chain(h http.Handler, mw ...Middleware) http.Handler {
	for _, m := range slices.Backward(mw) {
		h = m(h)
	}
	return h
}

// Route describes a registered endpoint, e.g. {GET /v1/notes/{id}}.
type Route struct {
	Method string
	Path   string
}

func (r Route) String() string { return r.Method + " " + r.Path }

// Router registers routes on an http.ServeMux. Groups share the mux and the
// route registry but carry their own prefix and middleware.
type Router struct {
	mux    *http.ServeMux
	log    *slog.Logger
	prefix string
	mw     []Middleware
	routes *[]Route
}

// NewRouter returns an empty Router. log is used to report handler errors.
func NewRouter(log *slog.Logger) *Router {
	return &Router{mux: http.NewServeMux(), log: log, routes: &[]Route{}}
}

// Group returns a sub-router whose routes are prefixed with prefix and
// wrapped by mw (after the parent's middleware).
func (rt *Router) Group(prefix string, mw ...Middleware) *Router {
	return &Router{
		mux:    rt.mux,
		log:    rt.log,
		prefix: rt.prefix + strings.TrimSuffix(prefix, "/"),
		mw:     append(slices.Clone(rt.mw), mw...),
		routes: rt.routes,
	}
}

// Handle registers h for pattern, written in ServeMux syntax relative to the
// group prefix: "GET /notes/{id}". A method is required.
func (rt *Router) Handle(pattern string, h HandlerFunc, mw ...Middleware) {
	rt.HandleHTTP(pattern, rt.adapt(h), mw...)
}

// HandleHTTP registers a plain http.Handler (for example a file server or a
// third-party handler) with the same prefix, middleware and registry rules.
func (rt *Router) HandleHTTP(pattern string, h http.Handler, mw ...Middleware) {
	method, path, ok := strings.Cut(pattern, " ")
	if !ok || method == "" || !strings.HasPrefix(path, "/") {
		panic("web: pattern must look like \"METHOD /path\", got " + pattern)
	}
	full := rt.prefix + path
	if path == "/" && rt.prefix != "" {
		full = rt.prefix
	}
	route := Route{Method: method, Path: full}
	*rt.routes = append(*rt.routes, route)

	all := append(slices.Clone(rt.mw), mw...)
	rt.mux.Handle(route.String(), Chain(tagRoute(route.String(), h), all...))
}

// Routes returns every registered route, in registration order.
func (rt *Router) Routes() []Route { return slices.Clone(*rt.routes) }

// ServeHTTP makes Router an http.Handler.
func (rt *Router) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	rt.mux.ServeHTTP(w, r)
}

// adapt converts a HandlerFunc into an http.Handler that renders errors.
func (rt *Router) adapt(h HandlerFunc) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := h(w, r); err != nil {
			WriteError(rt.log, w, r, err)
		}
	})
}

// tagRoute records the matched route pattern on the request info so outer
// middleware (access log, tracing, metrics) can label by route, not by raw
// path, which would explode metric cardinality.
func tagRoute(pattern string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if info := requestInfo(r.Context()); info != nil {
			info.Route = pattern
		}
		next.ServeHTTP(w, r)
	})
}
