package filter

import (
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/datakaveri/dx-common-go/platform/errors"
)

// Parse validates a request's query values against the spec and returns a
// storage-neutral Request, or a validation error (which platform/http maps to
// 400).
//
// It assumes unknown-parameter rejection has already happened at the transport
// layer (paging.Strict over the union of paging names and Spec.Names); Parse
// itself only reads the parameters it declares. Every failure mode here — empty
// value, over-cardinality, bad enum, malformed or inconsistent temporal input —
// is an error, never a silently dropped filter, because a dropped filter widens
// the result set.
func (s Spec) Parse(q url.Values) (Request, error) {
	req := Request{exact: make(map[Key][]string)}
	total := 0
	for api, f := range s.fields {
		vals, ok := q[api]
		if !ok {
			continue
		}
		clean, err := f.validate(vals)
		if err != nil {
			return Request{}, err
		}
		if len(clean) == 0 {
			continue
		}
		total += len(clean)
		req.exact[f.key] = clean
	}
	if total > MaxTotalValues {
		return Request{}, errors.Validation(fmt.Sprintf("too many filter values; at most %d across all filters", MaxTotalValues))
	}

	times, err := s.parseTimes(q)
	if err != nil {
		return Request{}, err
	}
	req.times = times
	return req, nil
}

func (s Spec) parseTimes(q url.Values) ([]TimeFilter, error) {
	var out []TimeFilter
	for _, t := range s.times {
		timeName, endName, relName := t.params()
		timeStr := strings.TrimSpace(q.Get(timeName))
		endStr := strings.TrimSpace(q.Get(endName))
		relStr := strings.TrimSpace(q.Get(relName))
		if timeStr == "" && endStr == "" && relStr == "" {
			continue // this temporal field was not queried
		}
		tf, err := buildTime(t.key, relStr, timeStr, endStr)
		if err != nil {
			return nil, err
		}
		out = append(out, tf)
	}
	return out, nil
}

// buildTime validates one temporal query and resolves it to a TimeFilter.
//
// The rules mirror the legacy Java TemporalRequestHelper, corrected: `during`
// aliases `between`, both range bounds are required and ordered, a relation is
// mandatory once any temporal parameter is present, and an unknown relation is
// rejected rather than silently dropped.
func buildTime(key Key, rel, timeStr, endStr string) (TimeFilter, error) {
	r, ok := normRel(rel)
	if !ok {
		if rel == "" {
			return TimeFilter{}, errors.Validation("timerel is required for a temporal query")
		}
		return TimeFilter{}, errors.Validation("timerel must be one of between, during, after, before")
	}
	switch r {
	case Between:
		if timeStr == "" || endStr == "" {
			return TimeFilter{}, errors.Validation("both time and endTime are required for '" + rel + "'")
		}
		from, err := parseTS(timeStr)
		if err != nil {
			return TimeFilter{}, err
		}
		to, err := parseTS(endStr)
		if err != nil {
			return TimeFilter{}, err
		}
		if from.After(to) {
			return TimeFilter{}, errors.Validation("time must be before or equal to endTime")
		}
		return TimeFilter{Key: key, Rel: Between, From: from, To: to}, nil
	case After, Before:
		if timeStr == "" {
			return TimeFilter{}, errors.Validation("time is required for '" + rel + "'")
		}
		from, err := parseTS(timeStr)
		if err != nil {
			return TimeFilter{}, err
		}
		return TimeFilter{Key: key, Rel: r, From: from}, nil
	default:
		return TimeFilter{}, errors.Validation("timerel must be one of between, during, after, before")
	}
}

// normRel lowercases the relation and folds during into between.
func normRel(rel string) (Rel, bool) {
	switch strings.ToLower(rel) {
	case "between", "during":
		return Between, true
	case "after":
		return After, true
	case "before":
		return Before, true
	default:
		return relUnset, false
	}
}

// tsLayouts are accepted timestamp formats, richest first: RFC3339 (with zone),
// then a zone-less local form for legacy compatibility, then a bare date. A
// zone-less value is read as UTC — a per-operation compatibility rule can be
// added later if a service needs local-time semantics.
var tsLayouts = []string{
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02",
}

func parseTS(s string) (time.Time, error) {
	for _, layout := range tsLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.Validation("invalid time format; expected RFC3339 (e.g. 2026-01-02T15:04:05Z)")
}
