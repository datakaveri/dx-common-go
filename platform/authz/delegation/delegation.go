// Package delegation is the ONE definition of how an agent delegation grant's
// resource allowlist matches a concrete resource id.
//
// # Why it lives here
//
// Three services evaluate the same allowlist, and they used to disagree:
//
//   - dx-mcp-gateway-go matched "rg-demo-*" against "rg-demo-product" by
//     boundary-anchored prefix — correctly.
//   - dx-authz-go projected the grant into OpenFGA as the LITERAL object
//     "resource:rg-demo-*", which no check against "resource:rg-demo-product"
//     can ever reach.
//   - dx-gateway-go's agent check therefore denied every wildcard grant the
//     moment it was switched on.
//
// OpenFGA has no prefix matching, so the projection cannot express a wildcard
// directly. The fix is to agree on an ENCODING: a wildcard pattern is written
// as one ordinary object id (ObjectID), and a checker asks about every encoded
// pattern that could cover the id it holds (Candidates). Match is the reference
// semantics both are tested against.
//
// Layer: L1 — pure functions, no I/O.
package delegation

import "strings"

// WildcardSuffix replaces a pattern's trailing "*" in its FGA object id.
//
// "*" itself is avoided because OpenFGA gives it meaning in the user position
// ("type:*") and an id carrying it invites a reader to assume the same here. "~"
// cannot appear in a DX resource id (they are URN/slug characters), so an
// encoded pattern can never collide with a real resource.
const WildcardSuffix = "~any"

// Delimiters are the characters treated as id-segment boundaries. A wildcard
// only ever extends across a boundary, so "rg-pune-1*" does NOT cover
// "rg-pune-10".
const Delimiters = "-_/.:"

// MaxCandidates bounds how many encoded patterns Candidates returns. A real DX
// id has a handful of segments; the bound exists so a hostile id made of
// delimiters cannot turn one authorization into hundreds of PDP calls. Ids that
// would exceed it yield the exact id plus the MOST SPECIFIC patterns, which can
// only make a check stricter, never looser.
const MaxCandidates = 24

// IsWildcard reports whether a grant entry is a wildcard pattern.
func IsWildcard(entry string) bool { return strings.HasSuffix(entry, "*") }

// splitType separates an optional "type:" qualifier from the id. Only the LAST
// ":" counts, matching the MCP gateway's historical behaviour for
// "resource:rg-1" entries.
func splitType(entry string) (typ, id string) {
	if i := strings.LastIndexByte(entry, ':'); i >= 0 {
		return entry[:i+1], entry[i+1:]
	}
	return "", entry
}

// ObjectID returns the id to write into OpenFGA for one grant entry: exact ids
// unchanged, a trailing "*" replaced by WildcardSuffix. Any "type:" qualifier
// is preserved.
func ObjectID(entry string) string {
	if !IsWildcard(entry) {
		return entry
	}
	return strings.TrimSuffix(entry, "*") + WildcardSuffix
}

// Match reports whether a grant entry covers a requested resource id.
//
// Accepted entry forms: an exact id, a type-qualified id ("resource:rid"), and
// a trailing-* prefix pattern ("rg-pune-*") — boundary-anchored: it matches the
// prefix exactly, or where the next character of the requested id (or the
// prefix's own last character) is a delimiter.
func Match(entry, requested string) bool {
	if requested == "" || entry == "" {
		return false
	}
	_, id := splitType(entry)
	for _, candidate := range []string{entry, id} {
		if candidate == requested {
			return true
		}
		if IsWildcard(candidate) && wildcardMatch(strings.TrimSuffix(candidate, "*"), requested) {
			return true
		}
	}
	return false
}

// MatchAny reports whether any entry in allow covers requested.
func MatchAny(allow []string, requested string) bool {
	for _, e := range allow {
		if Match(e, requested) {
			return true
		}
	}
	return false
}

func wildcardMatch(prefix, requested string) bool {
	if !strings.HasPrefix(requested, prefix) {
		return false
	}
	if len(requested) == len(prefix) {
		return true
	}
	if prefix != "" && strings.ContainsRune(Delimiters, rune(prefix[len(prefix)-1])) {
		return true
	}
	return strings.ContainsRune(Delimiters, rune(requested[len(prefix)]))
}

// Candidates returns every FGA object id (untyped) whose grant would cover id:
// the exact id first, then the encoded wildcard forms, most specific first.
//
// For "rg-demo-product" that is:
//
//	rg-demo-product, rg-demo-product~any, rg-demo-~any, rg-demo~any, rg-~any, rg~any
//
// A checker holding a concrete id asks "does the agent hold delegated_R on any
// of these?" — exactly the question Match answers against the raw grant.
func Candidates(id string) []string {
	if id == "" {
		return nil
	}
	out := []string{id, id + WildcardSuffix}
	seen := map[string]bool{out[0]: true, out[1]: true}
	add := func(s string) {
		if !seen[s] && len(out) < MaxCandidates {
			seen[s] = true
			out = append(out, s)
		}
	}
	// Walk boundaries right-to-left so the most specific patterns come first
	// and survive the MaxCandidates cut.
	for i := len(id) - 1; i >= 0; i-- {
		if !strings.ContainsRune(Delimiters, rune(id[i])) {
			continue
		}
		// prefix ending AT the delimiter: "rg-demo-*" covers "rg-demo-product".
		add(id[:i+1] + WildcardSuffix)
		// prefix ending BEFORE the delimiter: "rg-demo*" covers "rg-demo-product".
		// i == 0 gives the empty prefix ("*"), which Match also accepts for an
		// id starting with a delimiter — kept for exact parity.
		add(id[:i] + WildcardSuffix)
	}
	return out
}
