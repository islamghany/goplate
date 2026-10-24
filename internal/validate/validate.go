// Package validate is a tiny, reflection-free validation helper.
//
// Request DTOs implement Validate() error using a Validator, which keeps
// rules next to the type they protect and readable as plain Go:
//
//	func (r createNoteRequest) Validate() error {
//		v := validate.New()
//		v.Required("title", r.Title)
//		v.MaxLen("title", r.Title, 200)
//		return v.Err()
//	}
//
// It returns an *errs.Error of kind Invalid, which the web layer renders as
// a 400 with per-field messages. We deliberately avoid struct-tag validators:
// tags are untyped strings, need a global instance, and hide business rules.
package validate

import (
	"fmt"
	"net/mail"
	"strings"
	"unicode/utf8"

	"github.com/islamghany/goplate/internal/errs"
)

// Validator accumulates field errors. The zero value is not usable; call New.
type Validator struct {
	fields []errs.FieldError
	seen   map[string]bool
}

// New returns an empty Validator.
func New() *Validator {
	return &Validator{seen: map[string]bool{}}
}

// Check records msg for field when ok is false. Only the first failure per
// field is kept, so messages stay focused.
func (v *Validator) Check(ok bool, field, msg string) {
	if ok || v.seen[field] {
		return
	}
	v.seen[field] = true
	v.fields = append(v.fields, errs.FieldError{Field: field, Message: msg})
}

// Required fails when s is empty after trimming whitespace.
func (v *Validator) Required(field, s string) {
	v.Check(strings.TrimSpace(s) != "", field, "is required")
}

// MinLen fails when s has fewer than n characters (runes, not bytes).
func (v *Validator) MinLen(field, s string, n int) {
	v.Check(utf8.RuneCountInString(s) >= n, field, fmt.Sprintf("must be at least %d characters", n))
}

// MaxLen fails when s has more than n characters (runes, not bytes).
func (v *Validator) MaxLen(field, s string, n int) {
	v.Check(utf8.RuneCountInString(s) <= n, field, fmt.Sprintf("must be at most %d characters", n))
}

// Email fails when s is not a bare email address ("a@b.c", no display name).
func (v *Validator) Email(field, s string) {
	addr, err := mail.ParseAddress(s)
	v.Check(err == nil && addr.Address == s && strings.Contains(s, "."), field, "must be a valid email address")
}

// OneOf fails when s is not one of allowed.
func (v *Validator) OneOf(field, s string, allowed ...string) {
	for _, a := range allowed {
		if s == a {
			return
		}
	}
	v.Check(false, field, "must be one of: "+strings.Join(allowed, ", "))
}

// Between fails when n is outside [lo, hi].
func (v *Validator) Between(field string, n, lo, hi int) {
	v.Check(n >= lo && n <= hi, field, fmt.Sprintf("must be between %d and %d", lo, hi))
}

// Valid reports whether no checks have failed.
func (v *Validator) Valid() bool { return len(v.fields) == 0 }

// Err returns nil when valid, otherwise an *errs.Error of kind Invalid.
func (v *Validator) Err() error {
	if v.Valid() {
		return nil
	}
	return errs.Validation(v.fields...)
}
