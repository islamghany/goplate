// Package ratelimit defines the rate-limiting port and its HTTP middleware
// (ADR-0013). Adapters: internal/platform/memory (single instance, tests)
// and internal/platform/redis (GCRA, correct across many instances).
package ratelimit

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/identity"
	"github.com/islamghany/goplate/internal/web"
)

// Policy is a named limit: Rate requests per Period, allowing Burst at once.
type Policy struct {
	Name   string
	Rate   int
	Period time.Duration
	Burst  int
}

// Decision is the outcome of one Allow call.
type Decision struct {
	Allowed    bool
	Limit      int
	Remaining  int
	RetryAfter time.Duration // when not allowed: how long until a retry may succeed
	ResetAfter time.Duration // how long until the bucket is full again
}

// Limiter is the port adapters implement.
type Limiter interface {
	Allow(ctx context.Context, key string, p Policy) (Decision, error)
}

// KeyFunc derives the bucket key for a request. Returning "" skips limiting.
type KeyFunc func(r *http.Request) string

// ByUserOrIP keys authenticated requests by user and anonymous ones by IP,
// so users behind one NAT do not share a bucket once they log in.
func ByUserOrIP(r *http.Request) string {
	if p, ok := identity.FromContext(r.Context()); ok {
		return "user:" + p.UserID.String()
	}
	return "ip:" + web.ClientIP(r)
}

// ByIP keys every request by client IP.
func ByIP(r *http.Request) string { return "ip:" + web.ClientIP(r) }

// ErrLimited is rendered as 429.
var ErrLimited = errs.New(errs.RateLimited, "too many requests; slow down")

// Options control middleware behavior on limiter failures.
type Options struct {
	// FailClosed rejects requests when the limiter backend errors. The
	// default (fail open) keeps the API up during a Redis incident; use
	// fail-closed for brute-force-sensitive routes such as login.
	FailClosed bool
}

// Middleware enforces p per key. It sets the IETF RateLimit headers on every
// response and Retry-After on rejections.
func Middleware(log *slog.Logger, l Limiter, p Policy, key KeyFunc, opts Options) web.Middleware {
	policyHeader := fmt.Sprintf("%q;q=%d;w=%d", p.Name, p.Rate, int(p.Period.Seconds()))
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			k := key(r)
			if k == "" {
				next.ServeHTTP(w, r)
				return
			}
			d, err := l.Allow(r.Context(), p.Name+":"+k, p)
			if err != nil {
				log.WarnContext(r.Context(), "rate limiter unavailable", "policy", p.Name, "fail_closed", opts.FailClosed, "err", err)
				if opts.FailClosed {
					web.WriteError(log, w, r, errs.Wrap(errs.Unavailable, "", err))
					return
				}
				next.ServeHTTP(w, r)
				return
			}

			h := w.Header()
			h.Set("RateLimit-Policy", policyHeader)
			h.Set("RateLimit", fmt.Sprintf("%q;r=%d;t=%d", p.Name, d.Remaining, ceilSeconds(d.ResetAfter)))
			if !d.Allowed {
				h.Set("Retry-After", strconv.Itoa(ceilSeconds(d.RetryAfter)))
				web.WriteError(log, w, r, ErrLimited)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func ceilSeconds(d time.Duration) int {
	if d <= 0 {
		return 0
	}
	return int((d + time.Second - 1) / time.Second)
}
