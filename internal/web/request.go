package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
)

// RequestInfo is per-request metadata shared between middleware layers.
//
// It is stored as a pointer so inner layers (the router) can fill in fields
// that outer layers (access log, tracing) read after the handler returns.
// Only middleware in this package and platform adapters should touch it;
// handlers use the accessor functions below.
type RequestInfo struct {
	ID       string
	Route    string // matched pattern, e.g. "GET /v1/notes/{id}"; empty if unmatched
	ClientIP string
	Start    time.Time
}

type infoKey struct{}

func withRequestInfo(ctx context.Context, info *RequestInfo) context.Context {
	return context.WithValue(ctx, infoKey{}, info)
}

func requestInfo(ctx context.Context) *RequestInfo {
	info, _ := ctx.Value(infoKey{}).(*RequestInfo)
	return info
}

// RequestID returns the request ID, or "" outside a request.
func RequestID(ctx context.Context) string {
	if info := requestInfo(ctx); info != nil {
		return info.ID
	}
	return ""
}

// RoutePattern returns the matched route pattern, or "" if none matched.
func RoutePattern(ctx context.Context) string {
	if info := requestInfo(ctx); info != nil {
		return info.Route
	}
	return ""
}

// ClientIP returns the client address resolved by the RealIP middleware,
// falling back to the connection's remote address.
func ClientIP(r *http.Request) string {
	if info := requestInfo(r.Context()); info != nil && info.ClientIP != "" {
		return info.ClientIP
	}
	return remoteHost(r.RemoteAddr)
}

// Validator is implemented by request DTOs that validate themselves.
type Validator interface {
	Validate() error
}

// Decode reads a JSON request body into a T and, if T implements Validator,
// validates it. It is strict on purpose: unknown fields, trailing data and
// non-JSON content types are rejected, because silently ignoring a client's
// typo ("tittle") produces bugs that are very hard to track down.
//
// The body size limit is enforced by the MaxBytes middleware.
func Decode[T any](r *http.Request) (T, error) {
	var dst T

	if ct := r.Header.Get("Content-Type"); ct != "" {
		mt, _, err := mime.ParseMediaType(ct)
		if err != nil || (mt != "application/json" && !strings.HasSuffix(mt, "+json")) {
			return dst, errs.New(errs.Invalid, "Content-Type must be application/json")
		}
	}

	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&dst); err != nil {
		return dst, decodeError(err)
	}
	if err := dec.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return dst, errs.New(errs.Invalid, "request body must contain a single JSON value")
	}

	// &dst's method set includes value-receiver methods, so this covers both.
	if v, ok := any(&dst).(Validator); ok {
		if err := v.Validate(); err != nil {
			return dst, err
		}
	}
	return dst, nil
}

// decodeError turns encoding/json errors into client-friendly messages.
func decodeError(err error) error {
	var (
		syntaxErr *json.SyntaxError
		typeErr   *json.UnmarshalTypeError
		maxErr    *http.MaxBytesError
	)
	switch {
	case errors.Is(err, io.EOF):
		return errs.New(errs.Invalid, "request body must not be empty")
	case errors.Is(err, io.ErrUnexpectedEOF):
		return errs.New(errs.Invalid, "request body contains malformed JSON")
	case errors.As(err, &syntaxErr):
		return errs.New(errs.Invalid, fmt.Sprintf("request body contains malformed JSON (at byte %d)", syntaxErr.Offset))
	case errors.As(err, &typeErr):
		if typeErr.Field != "" {
			return errs.Validation(errs.FieldError{Field: typeErr.Field, Message: "must be a " + typeErr.Type.String()})
		}
		return errs.New(errs.Invalid, "request body has the wrong JSON type")
	case errors.As(err, &maxErr):
		return errs.New(errs.Invalid, fmt.Sprintf("request body must not be larger than %d bytes", maxErr.Limit))
	case strings.HasPrefix(err.Error(), "json: unknown field "):
		field := strings.Trim(strings.TrimPrefix(err.Error(), "json: unknown field "), `"`)
		return errs.Validation(errs.FieldError{Field: field, Message: "is not a known field"})
	default:
		return errs.Wrap(errs.Invalid, "request body could not be decoded", err)
	}
}

// PathUUID parses the named path wildcard as a UUID.
func PathUUID(r *http.Request, name string) (uuid.UUID, error) {
	id, err := uuid.Parse(r.PathValue(name))
	if err != nil {
		return uuid.Nil, errs.Validation(errs.FieldError{Field: name, Message: "must be a valid UUID"})
	}
	return id, nil
}

// QueryInt parses an optional integer query parameter.
func QueryInt(r *http.Request, name string, def int) (int, error) {
	s := r.URL.Query().Get(name)
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0, errs.Validation(errs.FieldError{Field: name, Message: "must be an integer"})
	}
	return n, nil
}
