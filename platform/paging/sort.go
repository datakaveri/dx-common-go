package paging

import (
	"strings"
)

// MaxSortKeys bounds how many fields a client may sort by in one request.
// Unbounded multi-key sorts are a cheap way to make the database do expensive
// work on an attacker's behalf.
const MaxSortKeys = 3

// Direction is a sort direction.
type Direction string

const (
	Asc  Direction = "asc"
	Desc Direction = "desc"
)

// SortKey is one requested sort field, as it arrived from the client.
//
// Field is UNTRUSTED — it is a raw string from the query string and must never
// reach a SQL identifier position. Resolving it to a column is the job of an
// allowlist in the storage layer (platform/database/sql.Sortable), which lives
// there rather than here because only that layer knows what a column is.
type SortKey struct {
	Field string
	Dir   Direction
}

// ParseSort parses the platform sort syntax:
//
//	?sort=created_at:desc;name:asc
//
// Semicolon-separated, `field[:dir]`, direction defaulting to asc. Keys beyond
// MaxSortKeys are rejected rather than silently truncated — quietly returning
// differently-ordered data than asked for is worse than an error.
//
// An empty or whitespace-only input yields nil with no error: no sort requested
// is not a bad request.
func ParseSort(raw string) ([]SortKey, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}

	parts := strings.Split(raw, ";")
	if len(parts) > MaxSortKeys {
		return nil, &SortError{Reason: "too many sort fields", Detail: "at most " + itoa(MaxSortKeys) + " are allowed"}
	}

	keys := make([]SortKey, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		field, dir, err := parseSortKey(p)
		if err != nil {
			return nil, err
		}
		// A repeated field is ambiguous — the second occurrence would be a
		// no-op in SQL, so the request does not mean what the client thinks.
		if _, dup := seen[field]; dup {
			return nil, &SortError{Reason: "duplicate sort field", Detail: field}
		}
		seen[field] = struct{}{}
		keys = append(keys, SortKey{Field: field, Dir: dir})
	}
	if len(keys) == 0 {
		return nil, nil
	}
	return keys, nil
}

func parseSortKey(p string) (string, Direction, error) {
	field, dirStr, hasDir := strings.Cut(p, ":")
	field = strings.TrimSpace(field)
	if field == "" {
		return "", "", &SortError{Reason: "empty sort field", Detail: p}
	}
	if !hasDir {
		return field, Asc, nil
	}
	switch strings.ToLower(strings.TrimSpace(dirStr)) {
	case "asc", "":
		return field, Asc, nil
	case "desc":
		return field, Desc, nil
	default:
		return "", "", &SortError{Reason: "invalid sort direction", Detail: dirStr + " (want asc or desc)"}
	}
}

// SortError is a malformed sort parameter.
//
// It is a distinct type rather than a platform error so that paging stays at L0
// with no dependency on the error taxonomy; platform/http maps it to a 400.
type SortError struct {
	Reason string
	Detail string
}

func (e *SortError) Error() string {
	if e.Detail == "" {
		return "paging: " + e.Reason
	}
	return "paging: " + e.Reason + ": " + e.Detail
}

// itoa avoids pulling strconv in for one small-int conversion.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
