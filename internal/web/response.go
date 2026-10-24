package web

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"

	"github.com/islamghany/goplate/internal/errs"
)

// JSON writes v as a JSON response with the given status.
//
// The body is marshaled before anything is written, so an encoding failure
// becomes a clean 500 instead of a half-written 200.
func JSON(w http.ResponseWriter, status int, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return errs.Wrap(errs.Internal, "", err)
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, err = w.Write(append(b, '\n'))
	return err
}

// NoContent writes a 204 response.
func NoContent(w http.ResponseWriter) error {
	w.WriteHeader(http.StatusNoContent)
	return nil
}

// Problem is an RFC 9457 "problem details" document.
type Problem struct {
	Type      string            `json:"type"`
	Title     string            `json:"title"`
	Status    int               `json:"status"`
	Detail    string            `json:"detail,omitempty"`
	Instance  string            `json:"instance,omitempty"`
	RequestID string            `json:"request_id,omitempty"`
	Errors    []errs.FieldError `json:"errors,omitempty"`
}

// StatusFor is the single mapping from error kinds to HTTP status codes.
func StatusFor(k errs.Kind) int {
	switch k {
	case errs.Invalid:
		return http.StatusBadRequest
	case errs.Unauthenticated:
		return http.StatusUnauthorized
	case errs.PermissionDenied:
		return http.StatusForbidden
	case errs.NotFound:
		return http.StatusNotFound
	case errs.Conflict:
		return http.StatusConflict
	case errs.PreconditionFailed:
		return http.StatusPreconditionFailed
	case errs.RateLimited:
		return http.StatusTooManyRequests
	case errs.Unavailable:
		return http.StatusServiceUnavailable
	default:
		return http.StatusInternalServerError
	}
}

// ProblemFor builds the Problem rendered for err. For 5xx responses the
// detail is always generic: internals (SQL, hostnames, stack traces) only
// go to logs and traces, correlated by request_id.
func ProblemFor(r *http.Request, err error) Problem {
	kind := errs.KindOf(err)
	status := StatusFor(kind)
	p := Problem{
		Type:      "/problems/" + kind.String(),
		Title:     http.StatusText(status),
		Status:    status,
		Instance:  r.URL.Path,
		RequestID: RequestID(r.Context()),
	}
	if e, ok := errs.As(err); ok && status < 500 {
		p.Detail = e.Msg
		p.Errors = e.Fields
	}
	if status >= 500 {
		p.Detail = "an unexpected error occurred; quote the request_id when reporting it"
		if kind == errs.Unavailable {
			p.Detail = "the service is temporarily unavailable; please retry"
		}
	}
	return p
}

// WriteError logs err at the right level and renders it as a problem.
func WriteError(log *slog.Logger, w http.ResponseWriter, r *http.Request, err error) {
	ctx := r.Context()

	// The client went away: nothing useful can be written, and it is not
	// a server error worth paging anyone about.
	if errors.Is(err, context.Canceled) && ctx.Err() != nil {
		log.DebugContext(ctx, "request canceled by client", "err", err)
		return
	}
	if errors.Is(err, context.DeadlineExceeded) && errs.KindOf(err) == errs.Internal {
		err = errs.Wrap(errs.Unavailable, "", err)
	}

	p := ProblemFor(r, err)
	if p.Status >= 500 {
		log.ErrorContext(ctx, "request failed", "status", p.Status, "err", err)
	} else {
		log.DebugContext(ctx, "request rejected", "status", p.Status, "err", err)
	}

	if rw, ok := w.(interface{ Written() bool }); ok && rw.Written() {
		return // headers already sent; the access log still records the failure
	}
	if p.Status == http.StatusUnauthorized {
		w.Header().Set("WWW-Authenticate", `Bearer realm="api"`)
	}
	b, _ := json.Marshal(p)
	w.Header().Set("Content-Type", "application/problem+json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(p.Status)
	_, _ = w.Write(append(b, '\n'))
}
