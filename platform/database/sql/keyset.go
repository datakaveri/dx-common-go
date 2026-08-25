package sql

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// KeysetCursor is the resume point of a keyset (seek) page: the sort-key and
// tiebreaker-id values of the last row already delivered.
//
// Keyset pagination stays O(page) on arbitrarily deep pages, unlike OFFSET which
// scans and discards everything before the page. Use it for large, append-heavy
// tables (audit/event logs, feature stores) — it is the platform-native cursor
// this DSL adds so a service no longer drops to the legacy repository for deep
// lists.
type KeysetCursor struct {
	Key any `json:"k"`
	ID  any `json:"id"`
}

// EncodeKeysetCursor renders a cursor as an opaque URL-safe token. The client
// echoes it back verbatim on the next page and never parses it.
func EncodeKeysetCursor(c KeysetCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeKeysetCursor parses a token from a previous page's NextCursor.
//
// A cursor is opaque client input, so a token that fails to decode is a client
// error, not a server fault: it returns errors.Validation (HTTP 400) with the
// codec error as a logged-only cause, so a mangled ?cursor= is never a 500.
func DecodeKeysetCursor(token string) (KeysetCursor, error) {
	var c KeysetCursor
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return c, errors.Validation("invalid cursor").WithCause(err)
	}
	if err := json.Unmarshal(b, &c); err != nil {
		return c, errors.Validation("invalid cursor").WithCause(err)
	}
	return c, nil
}

// KeysetPage is one keyset page plus the token for the next one; NextCursor is
// empty on the final page.
type KeysetPage[T any] struct {
	Items      []T
	NextCursor string
}

// keysetSeek is the seek predicate for rows strictly after cur in (key, id)
// order — a row-value comparison expressed with the DSL's own predicates:
//
//	(key > k) OR (key = k AND id > id)   -- ascending
//	(key < k) OR (key = k AND id < id)   -- descending
//
// id MUST be unique (usually the primary key) so the composite order is total;
// key may repeat.
func keysetSeek(keyCol, idCol Column, desc bool, cur KeysetCursor) Pred {
	after := Gt
	if desc {
		after = Lt
	}
	return Or(
		after(keyCol, cur.Key),
		And(Eq(keyCol, cur.Key), after(idCol, cur.ID)),
	)
}

// Keyset returns one keyset-paginated page ordered by (keyCol, idCol), applied
// on top of whatever the query already filters. cursor is "" for the first page,
// else a token from a previous page's NextCursor. cursorOf extracts the two
// ordering values from a row (the generic layer cannot know which fields map to
// keyCol/idCol).
//
// The page is fetched with limit+1 rows to detect a next page without a COUNT.
// Any Order previously set on the query is replaced: keyset ordering must be
// exactly (keyCol, idCol) for the seek to be correct.
func (q *Query[T]) Keyset(
	ctx context.Context,
	keyCol, idCol Column,
	desc bool,
	cursor string,
	limit int,
	cursorOf func(T) KeysetCursor,
) (KeysetPage[T], error) {
	if limit < 1 {
		limit = 1
	}
	c := *q
	if cursor != "" {
		cur, err := DecodeKeysetCursor(cursor)
		if err != nil {
			return KeysetPage[T]{}, err
		}
		c.preds = append(append([]Pred(nil), c.preds...), keysetSeek(keyCol, idCol, desc, cur))
	}
	c.orders = []Order{{Column: keyCol, Desc: desc}, {Column: idCol, Desc: desc}}
	c.limit = limit + 1
	c.offset = 0

	rows, err := c.Find(ctx)
	if err != nil {
		return KeysetPage[T]{}, err
	}

	page := KeysetPage[T]{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		page.NextCursor = EncodeKeysetCursor(cursorOf(page.Items[limit-1]))
	}
	return page, nil
}
