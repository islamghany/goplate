// Package page implements keyset ("cursor") pagination shared by all modules.
//
// Why not OFFSET? With OFFSET, rows inserted or deleted while a client pages
// cause duplicates or skipped items, and deep pages get slower linearly. A
// keyset cursor remembers the sort key of the last item returned
// (created_at, id) and asks for "rows strictly after this key", which is
// stable and uses the index. The cursor is opaque to clients so we can change
// its contents without breaking them.
package page

import (
	"encoding/base64"
	"encoding/json"
	"time"

	"github.com/google/uuid"

	"github.com/islamghany/goplate/internal/errs"
)

// Page size bounds applied by NewRequest.
const (
	DefaultLimit = 20
	MaxLimit     = 100
)

// Cursor is the position after which the next page starts.
type Cursor struct {
	CreatedAt time.Time `json:"t"`
	ID        uuid.UUID `json:"i"`
}

// Request is a page request coming from a client.
type Request struct {
	Limit int
	After *Cursor // nil means "first page"
}

// Page is a page of results.
type Page[T any] struct {
	Items      []T    `json:"items"`
	NextCursor string `json:"next_cursor,omitempty"` // empty when there are no more items
}

// ErrInvalidCursor is returned for cursors that fail to decode.
var ErrInvalidCursor = errs.New(errs.Invalid, "invalid pagination cursor")

// NewRequest builds a Request from raw client values, clamping the limit.
func NewRequest(limit int, cursor string) (Request, error) {
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	req := Request{Limit: limit}
	if cursor != "" {
		c, err := Decode(cursor)
		if err != nil {
			return Request{}, err
		}
		req.After = &c
	}
	return req, nil
}

// Encode returns the opaque string form of c.
func Encode(c Cursor) string {
	b, _ := json.Marshal(c) // cannot fail for this struct
	return base64.RawURLEncoding.EncodeToString(b)
}

// Decode parses a string produced by Encode.
func Decode(s string) (Cursor, error) {
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return Cursor{}, ErrInvalidCursor
	}
	var c Cursor
	if err := json.Unmarshal(b, &c); err != nil || c.ID == uuid.Nil {
		return Cursor{}, ErrInvalidCursor
	}
	return c, nil
}

// Build turns limit+1 fetched rows into a Page. Repositories fetch one
// extra row to learn whether another page exists without a COUNT query.
func Build[T any](rows []T, limit int, key func(T) Cursor) Page[T] {
	if len(rows) <= limit {
		return Page[T]{Items: nonNil(rows)}
	}
	items := rows[:limit]
	return Page[T]{Items: items, NextCursor: Encode(key(items[len(items)-1]))}
}

func nonNil[T any](s []T) []T {
	if s == nil {
		return []T{} // render as [] not null
	}
	return s
}
