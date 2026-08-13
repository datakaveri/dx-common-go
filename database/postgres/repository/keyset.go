package repository

import (
	"context"
	"encoding/base64"
	"encoding/json"

	"github.com/datakaveri/dx-common-go/database/postgres/query"
	"github.com/datakaveri/dx-common-go/platform/errors"
)

// KeysetCursor is the resume point of a keyset (seek) page: the sort-key and
// tiebreaker-id values of the last row already delivered. Keyset pagination
// stays O(page) on arbitrarily deep pages, unlike OFFSET which scans and
// discards everything before the page — use it for large, append-heavy
// tables (feature stores, logs, events).
type KeysetCursor struct {
	Key any `json:"k"`
	ID  any `json:"id"`
}

// EncodeKeysetCursor renders a cursor as an opaque URL-safe token.
func EncodeKeysetCursor(c KeysetCursor) string {
	b, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeKeysetCursor parses a token produced by EncodeKeysetCursor.
//
// A cursor is opaque client input echoed back from a previous page, so a token
// that fails to decode is a client error, not a server fault: the failure is
// returned as errors.Validation (HTTP 400) with the codec error as a logged-
// only cause. Every FindKeyset adopter inherits that mapping — a mangled
// ?cursor= never surfaces as a 500.
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

// KeysetCondition renders the seek predicate for rows strictly after cur in
// (keyCol, idCol) order:
//
//	(keyCol > k) OR (keyCol = k AND idCol > id)      -- ascending
//	(keyCol < k) OR (keyCol = k AND idCol < id)      -- descending
//
// idCol must be unique (usually the primary key) so the composite order is
// total; keyCol may repeat.
func KeysetCondition(keyCol, idCol string, desc bool, cur KeysetCursor) query.Condition {
	op := query.OpGt
	if desc {
		op = query.OpLt
	}
	return query.Condition{Op: query.OpOr, Sub: []query.Condition{
		{Column: keyCol, Op: op, Value: cur.Key},
		{Op: query.OpAnd, Sub: []query.Condition{
			{Column: keyCol, Op: query.OpEq, Value: cur.Key},
			{Column: idCol, Op: op, Value: cur.ID},
		}},
	}}
}

// KeysetPage is one keyset page plus the cursor for the next one; NextCursor
// is empty on the final page.
type KeysetPage[R any] struct {
	Items      []R
	NextCursor string
}

// FindKeyset returns one keyset-paginated page ordered by (keyCol, idCol).
// cursor is "" for the first page, else a token from a previous page's
// NextCursor. cursorOf extracts the two ordering values from a row (the
// generic layer cannot know which struct fields map to keyCol/idCol).
//
// The page is fetched with limit+1 rows to detect whether a next page exists
// without a COUNT.
func (b *Base[R]) FindKeyset(
	ctx context.Context,
	conditions []query.Condition,
	keyCol, idCol string,
	desc bool,
	cursor string,
	limit int,
	cursorOf func(R) KeysetCursor,
) (KeysetPage[R], error) {
	if limit < 1 {
		limit = 1
	}
	if cursor != "" {
		cur, err := DecodeKeysetCursor(cursor)
		if err != nil {
			return KeysetPage[R]{}, err
		}
		conditions = append(conditions, KeysetCondition(keyCol, idCol, desc, cur))
	}

	f := b.Query(ctx).Where(conditions...).Limit(limit + 1)
	if desc {
		f = f.OrderByDesc(keyCol).OrderByDesc(idCol)
	} else {
		f = f.OrderBy(keyCol).OrderBy(idCol)
	}
	rows, err := f.Find(ctx)
	if err != nil {
		return KeysetPage[R]{}, err
	}

	page := KeysetPage[R]{Items: rows}
	if len(rows) > limit {
		page.Items = rows[:limit]
		page.NextCursor = EncodeKeysetCursor(cursorOf(page.Items[limit-1]))
	}
	return page, nil
}
