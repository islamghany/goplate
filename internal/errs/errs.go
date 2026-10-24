// Package errs is the application's transport-agnostic error vocabulary.
//
// Services describe *what kind* of failure happened (not found, conflict,
// invalid input, ...) and never pick HTTP status codes. Transports (today
// internal/web, tomorrow maybe gRPC) own the single mapping from Kind to
// their own status codes. See ADR-0018.
//
// This package must stay dependency-free: every module imports it.
package errs

import (
	"errors"
	"strings"
)

// Kind classifies an error so transports can render it consistently.
type Kind uint8

// The zero value is Internal on purpose: an unclassified error is a bug and
// must never leak details to clients.
const (
	Internal           Kind = iota // unexpected failure; details are never shown to clients
	Invalid                        // the request is malformed or fails validation
	Unauthenticated                // missing or invalid credentials
	PermissionDenied               // authenticated, but not allowed
	NotFound                       // the resource does not exist (or is hidden from the caller)
	Conflict                       // the request conflicts with current state (duplicate, ...)
	PreconditionFailed             // optimistic-concurrency check failed
	RateLimited                    // the caller exceeded a rate limit
	Unavailable                    // a dependency is temporarily unavailable; retrying may help
)

var kindNames = [...]string{
	Internal:           "internal",
	Invalid:            "invalid",
	Unauthenticated:    "unauthenticated",
	PermissionDenied:   "permission_denied",
	NotFound:           "not_found",
	Conflict:           "conflict",
	PreconditionFailed: "precondition_failed",
	RateLimited:        "rate_limited",
	Unavailable:        "unavailable",
}

func (k Kind) String() string {
	if int(k) < len(kindNames) {
		return kindNames[k]
	}
	return "unknown"
}

// FieldError describes one invalid input field.
type FieldError struct {
	Field   string `json:"field"`
	Message string `json:"message"`
}

// Error is the concrete error type carried through the application.
//
// Msg is safe to show to API clients for every Kind except Internal. Err is
// the wrapped cause and is only ever logged.
type Error struct {
	Kind   Kind
	Msg    string
	Fields []FieldError
	Err    error
}

// New returns an error of the given kind with a client-safe message. Use it
// to declare package-level sentinels:
//
//	var ErrNotFound = errs.New(errs.NotFound, "note not found")
func New(kind Kind, msg string) *Error {
	return &Error{Kind: kind, Msg: msg}
}

// Wrap attaches a kind and a client-safe message to an underlying cause.
// The cause is preserved for errors.Is/As and logging, but never rendered.
func Wrap(kind Kind, msg string, err error) *Error {
	return &Error{Kind: kind, Msg: msg, Err: err}
}

// Validation returns an Invalid error listing the offending fields.
func Validation(fields ...FieldError) *Error {
	return &Error{Kind: Invalid, Msg: "the request contains invalid fields", Fields: fields}
}

func (e *Error) Error() string {
	var b strings.Builder
	b.WriteString(e.Kind.String())
	if e.Msg != "" {
		b.WriteString(": ")
		b.WriteString(e.Msg)
	}
	if e.Err != nil {
		b.WriteString(": ")
		b.WriteString(e.Err.Error())
	}
	return b.String()
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of the first *Error in err's chain, or Internal.
func KindOf(err error) Kind {
	if e, ok := errors.AsType[*Error](err); ok {
		return e.Kind
	}
	return Internal
}

// As returns the first *Error in err's chain.
func As(err error) (*Error, bool) {
	return errors.AsType[*Error](err)
}

// Is reports whether err is classified as kind.
func Is(err error, kind Kind) bool {
	return err != nil && KindOf(err) == kind
}
