package page_test

import (
	"testing"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
	"github.com/islamghany/goplate/internal/page"
)

func TestCursorRoundTrip(t *testing.T) {
	c := page.Cursor{CreatedAt: time.Date(2026, 1, 2, 3, 4, 5, 6000, time.UTC), ID: uuid.Must(uuid.NewV7())}
	got, err := page.Decode(page.Encode(c))
	if err != nil {
		t.Fatal(err)
	}
	if !got.CreatedAt.Equal(c.CreatedAt) || got.ID != c.ID {
		t.Fatalf("round trip mismatch: %+v vs %+v", got, c)
	}
}

func TestDecodeRejectsGarbage(t *testing.T) {
	for _, s := range []string{"!!!", "e30", "bm90LWpzb24"} { // invalid b64, "{}", "not-json"
		if _, err := page.Decode(s); !errs.Is(err, errs.Invalid) {
			t.Errorf("Decode(%q) = %v, want Invalid", s, err)
		}
	}
}

func TestNewRequestClampsLimit(t *testing.T) {
	for in, want := range map[int]int{0: page.DefaultLimit, -5: page.DefaultLimit, 7: 7, 1000: page.MaxLimit} {
		r, err := page.NewRequest(in, "")
		if err != nil || r.Limit != want || r.After != nil {
			t.Errorf("NewRequest(%d) = %+v, %v; want limit %d", in, r, err, want)
		}
	}
}

func TestBuild(t *testing.T) {
	type row struct{ n int }
	key := func(r row) page.Cursor { return page.Cursor{ID: uuid.New(), CreatedAt: time.Unix(int64(r.n), 0)} }

	last := page.Build([]row{{1}, {2}}, 2, key)
	if len(last.Items) != 2 || last.NextCursor != "" {
		t.Fatalf("exactly limit rows means last page: %+v", last)
	}

	more := page.Build([]row{{1}, {2}, {3}}, 2, key)
	if len(more.Items) != 2 || more.NextCursor == "" {
		t.Fatalf("limit+1 rows means another page: %+v", more)
	}
	c, err := page.Decode(more.NextCursor)
	if err != nil || c.CreatedAt.Unix() != 2 {
		t.Fatalf("cursor must point at the last returned item, got %+v %v", c, err)
	}

	empty := page.Build[row](nil, 2, key)
	if empty.Items == nil {
		t.Fatal("empty pages must render as [] not null")
	}
}
