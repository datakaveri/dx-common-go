package repository

import (
	"encoding/base64"
	"encoding/json"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// EncodeSearchAfter renders a hit's Sort values (Hit.Sort) as an opaque cursor
// token for the next SearchAfter page. It is the Elasticsearch sibling of the
// SQL keyset codec (database/postgres/repository.EncodeKeysetCursor): the same
// base64url(JSON) shape and the same opaque contract — the client echoes the
// token back untouched and never parses it, so the encoding is free to change.
func EncodeSearchAfter(sort []any) string {
	b, _ := json.Marshal(sort)
	return base64.RawURLEncoding.EncodeToString(b)
}

// DecodeSearchAfter parses a token from EncodeSearchAfter back into the sort
// values that seed SearchBuilder.SearchAfter / SearchRequest.SearchAfter.
//
// A search_after cursor is opaque client input echoed from a previous page, so
// a token that fails to decode is a client error, not a server fault: it
// returns errors.Validation (HTTP 400 through the envelope) with the codec
// error as a logged-only cause, never a 500. Every ES SearchAfter adopter
// inherits that mapping — a mangled ?cursor= cannot surface as a 500.
func DecodeSearchAfter(token string) ([]any, error) {
	b, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return nil, errors.Validation("invalid cursor").WithCause(err)
	}
	var sort []any
	if err := json.Unmarshal(b, &sort); err != nil {
		return nil, errors.Validation("invalid cursor").WithCause(err)
	}
	return sort, nil
}

// NextSearchAfter returns the Sort values of the last hit — the cursor for the
// following SearchAfter page — or nil when the result has no hits. The request
// must have set Sort (a total order) for the hits to carry Sort values.
func (r *SearchResult) NextSearchAfter() []any {
	if r == nil || len(r.Hits) == 0 {
		return nil
	}
	return r.Hits[len(r.Hits)-1].Sort
}
