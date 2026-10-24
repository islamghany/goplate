package errs_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/islamghany/goplate/internal/errs"
)

func TestKindOf(t *testing.T) {
	sentinel := errs.New(errs.NotFound, "thing not found")
	tests := []struct {
		name string
		err  error
		want errs.Kind
	}{
		{"nil-like plain error defaults to internal", errors.New("boom"), errs.Internal},
		{"sentinel", sentinel, errs.NotFound},
		{"wrapped with fmt", fmt.Errorf("loading: %w", sentinel), errs.NotFound},
		{"wrap keeps outer kind", errs.Wrap(errs.Conflict, "dup", errors.New("23505")), errs.Conflict},
		{"validation", errs.Validation(errs.FieldError{Field: "a", Message: "b"}), errs.Invalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := errs.KindOf(tt.err); got != tt.want {
				t.Fatalf("KindOf() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWrapPreservesCause(t *testing.T) {
	cause := errors.New("connection refused")
	err := errs.Wrap(errs.Unavailable, "try later", cause)
	if !errors.Is(err, cause) {
		t.Fatal("errors.Is should find the wrapped cause")
	}
	if !errs.Is(err, errs.Unavailable) {
		t.Fatal("errs.Is should match the kind")
	}
	if got := err.Error(); got != "unavailable: try later: connection refused" {
		t.Fatalf("Error() = %q", got)
	}
}
