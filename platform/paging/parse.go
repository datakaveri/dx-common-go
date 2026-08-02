package paging

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// Query parameter names. These are the platform contract: page/size, not
// offset/limit. Three parsers disagreed about this, which is how 10 of 11
// OpenAPI specs came to document offset/limit while one documented page/size.
const (
	ParamPage = "page"
	ParamSize = "size"
	ParamSort = "sort"
)

// Params is a parsed list request: the page window plus any requested sort.
type Params struct {
	Request
	Sort []SortKey
}

// Parse reads page, size and sort from an inbound request.
//
// It takes *http.Request rather than url.Values on purpose. Since Go 1.17,
// url.ParseQuery REJECTS a raw semicolon, and (*http.Request).URL.Query()
// swallows that error and returns only the parameters it could parse — so the
// platform's documented multi-field sort syntax
//
//	?sort=name:asc;created_at:desc
//
// silently loses the ENTIRE sort parameter when read through url.Values, and
// the endpoint returns unsorted data with no error. Reading the sort key from
// RawQuery is the only way to honour the documented syntax. (The existing
// request.Builder works around the same trap the same way.)
//
// Malformed page/size values are an error rather than a silent fallback to the
// default: quietly serving page 1 for ?page=abc is how a client ends up
// paginating in a loop over data it never sees.
//
// Well-formed but out-of-range values ARE clamped rather than rejected —
// ?size=1000 means "as many as you'll give me", which MaxSize answers honestly.
func Parse(r *http.Request) (Params, error) {
	return parse(r.URL.Query(), rawParam(r.URL.RawQuery, ParamSort))
}

// ParseValues is Parse for a caller that already holds url.Values and knows it
// has no multi-field sort to preserve — a single-key ?sort=name is unaffected
// by the semicolon problem, because there is no separator in the value.
//
// Prefer Parse. Reach for this only where no *http.Request exists.
func ParseValues(q url.Values) (Params, error) {
	return parse(q, q.Get(ParamSort))
}

func parse(q url.Values, rawSort string) (Params, error) {
	page, err := intParam(q, ParamPage, DefaultPage)
	if err != nil {
		return Params{}, err
	}
	size, err := intParam(q, ParamSize, DefaultSize)
	if err != nil {
		return Params{}, err
	}
	keys, err := ParseSort(rawSort)
	if err != nil {
		return Params{}, err
	}
	return Params{Request: NewRequest(page, size), Sort: keys}, nil
}

// Strict is Parse plus rejection of unknown query parameters.
//
// known lists every parameter the endpoint understands (filters, projections,
// and so on); page/size/sort are always allowed. Rejecting the unknown turns a
// client's typo — ?statuss=ACTIVE silently returning everything — into an error
// instead of a subtly wrong result set.
func Strict(r *http.Request, known ...string) (Params, error) {
	q := r.URL.Query()
	if err := rejectUnknown(q, known); err != nil {
		return Params{}, err
	}
	return parse(q, rawParam(r.URL.RawQuery, ParamSort))
}

func rejectUnknown(q url.Values, known []string) error {
	allowed := make(map[string]struct{}, len(known)+3)
	for _, k := range []string{ParamPage, ParamSize, ParamSort} {
		allowed[k] = struct{}{}
	}
	for _, k := range known {
		allowed[k] = struct{}{}
	}
	var unknown []string
	for k := range q {
		if _, ok := allowed[k]; !ok {
			unknown = append(unknown, k)
		}
	}
	if len(unknown) == 0 {
		return nil
	}
	sortStrings(unknown) // deterministic message; map iteration is random
	return &ParamError{Param: strings.Join(unknown, ", "), Reason: "unknown query parameter"}
}

func intParam(q url.Values, name string, def int) (int, error) {
	raw := strings.TrimSpace(q.Get(name))
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, &ParamError{Param: name, Reason: "must be an integer", Value: raw}
	}
	return n, nil
}

// rawParam pulls one parameter straight out of the raw query string, bypassing
// url.ParseQuery's semicolon rejection. See Parse for why this is necessary.
func rawParam(rawQuery, key string) string {
	for _, pair := range strings.Split(rawQuery, "&") {
		name, value, ok := strings.Cut(pair, "=")
		if !ok || name != key {
			continue
		}
		if unescaped, err := url.QueryUnescape(value); err == nil {
			return unescaped
		}
		return value
	}
	return ""
}

// ParamError is a malformed pagination parameter. platform/http maps it to 400.
type ParamError struct {
	Param  string
	Value  string
	Reason string
}

func (e *ParamError) Error() string {
	msg := "paging: " + e.Param + ": " + e.Reason
	if e.Value != "" {
		msg += " (got " + strconv.Quote(e.Value) + ")"
	}
	return msg
}

// sortStrings is an insertion sort — the slice is a handful of parameter names,
// so pulling in the sort package for it is not worth the dependency.
func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
