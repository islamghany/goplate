package validate_test

import (
	"testing"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/validate"
)

func TestValidator(t *testing.T) {
	v := validate.New()
	v.Required("name", "  ")
	v.MaxLen("name", "this would be a second error for name", 3) // only the first per field is kept
	v.Email("email", "not-an-email")
	v.MinLen("password", "short", 12)
	v.OneOf("role", "root", "user", "admin")
	v.Between("limit", 500, 1, 100)

	err := v.Err()
	e, ok := errs.As(err)
	if !ok || e.Kind != errs.Invalid {
		t.Fatalf("want Invalid *errs.Error, got %v", err)
	}
	want := map[string]string{
		"name":     "is required",
		"email":    "must be a valid email address",
		"password": "must be at least 12 characters",
		"role":     "must be one of: user, admin",
		"limit":    "must be between 1 and 100",
	}
	if len(e.Fields) != len(want) {
		t.Fatalf("got %d field errors, want %d: %+v", len(e.Fields), len(want), e.Fields)
	}
	for _, f := range e.Fields {
		if want[f.Field] != f.Message {
			t.Errorf("%s: got %q, want %q", f.Field, f.Message, want[f.Field])
		}
	}
}

func TestValidatorValid(t *testing.T) {
	v := validate.New()
	v.Required("name", "Ada")
	v.Email("email", "ada@example.com")
	v.MaxLen("name", "Ada", 3) // runes, inclusive
	if err := v.Err(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEmailRejectsDisplayNames(t *testing.T) {
	v := validate.New()
	v.Email("email", "Ada <ada@example.com>")
	if v.Valid() {
		t.Fatal("display-name form must be rejected")
	}
}
