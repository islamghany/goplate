package web

import (
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"regexp"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/islamghany/goplate/internal/errs"
)

// responseWriter records what the handler wrote, for logs and metrics.
// It implements Unwrap so http.ResponseController can reach the underlying
// writer's optional interfaces (Flusher, Hijacker, deadlines).
type responseWriter struct {
	http.ResponseWriter
	status  int
	bytes   int64
	written bool
}

func (rw *responseWriter) WriteHeader(code int) {
	if !rw.written {
		rw.status = code
		rw.written = true
	}
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	if !rw.written {
		rw.WriteHeader(http.StatusOK)
	}
	n, err := rw.ResponseWriter.Write(b)
	rw.bytes += int64(n)
	return n, err
}

func (rw *responseWriter) Unwrap() http.ResponseWriter { return rw.ResponseWriter }

// Written reports whether the status line has been sent.
func (rw *responseWriter) Written() bool { return rw.written }

var validRequestID = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// RequestIDMiddleware assigns every request an ID (reusing a well-formed incoming
// X-Request-ID so IDs can be correlated across services), exposes it in the
// X-Request-ID response header, and installs the RequestInfo used by the
// other middleware. It must be the outermost goplate middleware.
func RequestIDMiddleware() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			id := r.Header.Get("X-Request-ID")
			if !validRequestID.MatchString(id) {
				id = rand.Text() // 26 chars, 130 bits of randomness
			}
			w.Header().Set("X-Request-ID", id)
			info := &RequestInfo{ID: id, Start: time.Now()}
			next.ServeHTTP(w, r.WithContext(withRequestInfo(r.Context(), info)))
		})
	}
}

// AccessLog logs one line per request with route, status, size and latency.
// Health probes are logged at debug level to keep logs readable.
func AccessLog(log *slog.Logger, quietPaths ...string) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			level := slog.LevelInfo
			switch {
			case rw.status >= 500:
				level = slog.LevelError
			case slices.Contains(quietPaths, r.URL.Path):
				level = slog.LevelDebug
			}
			log.LogAttrs(r.Context(), level, "http request",
				slog.String("method", r.Method),
				slog.String("route", RoutePattern(r.Context())),
				slog.String("path", r.URL.Path),
				slog.Int("status", rw.status),
				slog.Int64("bytes", rw.bytes),
				slog.Duration("duration", time.Since(start)),
				slog.String("client_ip", ClientIP(r)),
				slog.String("user_agent", r.UserAgent()),
			)
		})
	}
}

// Recover turns a panic into a logged 500 so one bad request cannot crash
// the process. http.ErrAbortHandler is re-raised: it is net/http's way of
// deliberately aborting a response.
func Recover(log *slog.Logger) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			rw, ok := w.(*responseWriter)
			if !ok {
				rw = &responseWriter{ResponseWriter: w, status: http.StatusOK}
			}
			defer func() {
				v := recover()
				if v == nil {
					return
				}
				if err, ok := v.(error); ok && errors.Is(err, http.ErrAbortHandler) {
					panic(v)
				}
				log.ErrorContext(r.Context(), "panic recovered",
					"panic", fmt.Sprint(v), "stack", string(debug.Stack()))
				if rw.Written() {
					// Too late for a clean error: abort so the client sees
					// a broken response instead of a truncated "success".
					panic(http.ErrAbortHandler)
				}
				WriteError(log, rw, r, errs.Wrap(errs.Internal, "", fmt.Errorf("panic: %v", v)))
			}()
			next.ServeHTTP(rw, r)
		})
	}
}

// MaxBytes caps request bodies at n bytes. Handlers that accept uploads can
// raise the limit locally with http.MaxBytesReader.
func MaxBytes(n int64) Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			r.Body = http.MaxBytesReader(w, r.Body, n)
			next.ServeHTTP(w, r)
		})
	}
}

// SecurityHeaders sets conservative defaults suitable for a JSON API.
func SecurityHeaders() Middleware {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := w.Header()
			h.Set("X-Content-Type-Options", "nosniff")
			h.Set("X-Frame-Options", "DENY")
			h.Set("Referrer-Policy", "no-referrer")
			next.ServeHTTP(w, r)
		})
	}
}

// RealIP resolves the client IP. Forwarding headers are trusted ONLY when
// the direct peer is one of trusted (your load balancer / ingress);
// otherwise any client could spoof X-Forwarded-For and dodge rate limits.
// The client is the right-most address in X-Forwarded-For that is not a
// trusted proxy.
func RealIP(trusted []netip.Prefix) Middleware {
	isTrusted := func(a netip.Addr) bool {
		return slices.ContainsFunc(trusted, func(p netip.Prefix) bool { return p.Contains(a) })
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ip := remoteHost(r.RemoteAddr)
			if peer, err := netip.ParseAddr(ip); err == nil && isTrusted(peer.Unmap()) {
				hops := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
				for _, hop := range slices.Backward(hops) {
					a, err := netip.ParseAddr(strings.TrimSpace(hop))
					if err != nil {
						break
					}
					ip = a.Unmap().String()
					if !isTrusted(a.Unmap()) {
						break
					}
				}
			}
			if info := requestInfo(r.Context()); info != nil {
				info.ClientIP = ip
			}
			next.ServeHTTP(w, r)
		})
	}
}

func remoteHost(addr string) string {
	if host, _, err := net.SplitHostPort(addr); err == nil {
		return host
	}
	return addr
}

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	AllowedOrigins   []string // exact origins, or "*" for any (disables credentials)
	AllowedMethods   []string
	AllowedHeaders   []string
	ExposedHeaders   []string
	AllowCredentials bool
	MaxAge           time.Duration
}

// CORS implements the CORS protocol for an explicit origin allow-list.
//
// Unlike a blanket "Access-Control-Allow-Origin: *", it echoes the matching
// origin, adds Vary: Origin, and never combines a wildcard with credentials
// (browsers reject that combination anyway).
func CORS(cfg CORSConfig) Middleware {
	anyOrigin := slices.Contains(cfg.AllowedOrigins, "*")
	credentials := cfg.AllowCredentials && !anyOrigin
	methods := strings.Join(cfg.AllowedMethods, ", ")
	headers := strings.Join(cfg.AllowedHeaders, ", ")
	exposed := strings.Join(cfg.ExposedHeaders, ", ")
	maxAge := strconv.Itoa(int(cfg.MaxAge.Seconds()))

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			origin := r.Header.Get("Origin")
			h := w.Header()
			h.Add("Vary", "Origin")
			if origin == "" || (!anyOrigin && !slices.Contains(cfg.AllowedOrigins, origin)) {
				next.ServeHTTP(w, r)
				return
			}

			if anyOrigin && !credentials {
				h.Set("Access-Control-Allow-Origin", "*")
			} else {
				h.Set("Access-Control-Allow-Origin", origin)
			}
			if credentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			}

			// Preflight request: answer it here; it never reaches handlers.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
				h.Set("Access-Control-Allow-Methods", methods)
				h.Set("Access-Control-Allow-Headers", headers)
				h.Set("Access-Control-Max-Age", maxAge)
				w.WriteHeader(http.StatusNoContent)
				return
			}
			if exposed != "" {
				h.Set("Access-Control-Expose-Headers", exposed)
			}
			next.ServeHTTP(w, r)
		})
	}
}

// CrossOriginProtection rejects cross-origin, state-changing browser
// requests (CSRF) using Go's built-in http.CrossOriginProtection, which
// checks Sec-Fetch-Site / Origin (ADR-0008, ADR-0020). Non-browser clients
// send neither header and are unaffected. trustedOrigins are allowed
// cross-origin callers, typically your frontend's origin.
func CrossOriginProtection(log *slog.Logger, trustedOrigins []string) (Middleware, error) {
	cop := http.NewCrossOriginProtection()
	for _, o := range trustedOrigins {
		if o == "*" {
			continue
		}
		if err := cop.AddTrustedOrigin(o); err != nil {
			return nil, fmt.Errorf("trusted origin %q: %w", o, err)
		}
	}
	cop.SetDenyHandler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		WriteError(log, w, r, errs.New(errs.PermissionDenied, "cross-origin request rejected"))
	}))
	return cop.Handler, nil
}
