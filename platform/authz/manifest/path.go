package manifest

import (
	"errors"
	"net/url"
	"strings"
)

// Canonical path normalization (target design §5.20 #8, normative).
//
// # Why this is its own file with its own fuzz corpus
//
// The matcher decides which OPERATION a request is, and the operation decides
// whether authentication is required and which permission is checked. So a path
// that normalizes one way here and another way in the upstream is not a routing
// bug — it is a request being authorized as one operation and executed as
// another. Every rule below exists because some encoding makes two different
// paths look like the same one, or the same path look like two.
//
// The rules are normative and are reproduced from the design:
//
//	percent-decode ONCE, then REJECT any path whose decoded form contains an
//	encoded slash (%2F) in a template segment; collapse duplicate slashes;
//	resolve and REJECT dot segments rather than normalizing them away; treat
//	/x and /x/ as the same operation; match on the decoded, normalized form only.
//
// "Reject rather than normalize" is the load-bearing choice in two of those. A
// normalizer that silently turns `/a/../b` into `/b` has decided what the client
// meant; a rejecter has not. Since the thing being decided is which
// authorization rule applies, guessing is the wrong tool.
//
// # What this deliberately does NOT do
//
// It does not lowercase. Paths are case-sensitive in HTTP, and folding them here
// would make `/Item` and `/item` one operation while the upstream treats them as
// two — the same divergence in the other direction.

// Errors from Normalize. Distinct so a caller can log which rule refused, and so
// the corpus can assert the reason rather than just the refusal.
var (
	// ErrNotAbsolute is a path that does not begin with "/".
	ErrNotAbsolute = errors.New("manifest: path must be absolute")
	// ErrEncodedSlash is %2F (or %2f) in the raw path.
	ErrEncodedSlash = errors.New("manifest: encoded slash in path")
	// ErrDotSegment is "." or ".." as a path segment.
	ErrDotSegment = errors.New("manifest: dot segment in path")
	// ErrBadEncoding is a malformed percent-escape.
	ErrBadEncoding = errors.New("manifest: invalid percent-encoding")
	// ErrControlChar is a control character or NUL in the decoded path.
	ErrControlChar = errors.New("manifest: control character in path")
	// ErrTooLong exceeds maxPathLen.
	ErrTooLong = errors.New("manifest: path too long")
)

// maxPathLen bounds what the matcher will consider.
//
// Not a tuning parameter: an unbounded path is unbounded work in every step
// below, on an unauthenticated code path, before any rate limit has been
// consulted.
const maxPathLen = 2048

// Normalize canonicalises a request path, or refuses it.
//
// The returned form is what the matcher compares against, and the ONLY form it
// compares against — matching on anything else reintroduces the class of bug
// this function exists to remove.
func Normalize(raw string) (string, error) {
	if len(raw) > maxPathLen {
		return "", ErrTooLong
	}
	if raw == "" || raw[0] != '/' {
		return "", ErrNotAbsolute
	}

	// Encoded slash is refused BEFORE decoding, on the raw text.
	//
	// After decoding, %2F is indistinguishable from a real "/" — so a check
	// afterwards cannot tell `/a%2Fb` (one segment containing a slash) from
	// `/a/b` (two segments). Those are different operations, and letting them
	// collapse is how a request gets authorized as one and routed as the other.
	if containsFold(raw, "%2f") {
		return "", ErrEncodedSlash
	}

	// Decode exactly ONCE. Decoding twice would let `%252F` become `%2F` and
	// then "/", smuggling a separator past the check above — the classic
	// double-decode bypass.
	decoded, err := url.PathUnescape(raw)
	if err != nil {
		return "", ErrBadEncoding
	}

	// Control characters and NUL are refused rather than stripped. A stripped
	// NUL means this layer and the upstream disagree about where the path ends,
	// which is the same authorize-one-thing-execute-another failure.
	for _, r := range decoded {
		if r < 0x20 || r == 0x7f {
			return "", ErrControlChar
		}
	}

	// A residual "%" after decoding is refused, and the fuzzer is why.
	//
	// It found that `/a%252Fb` decodes once to `/a%2Fb` — a canonical form that
	// CANNOT ITSELF BE NORMALIZED, because a second pass reads that text as an
	// encoded slash and rejects it. A canonical form that is not a valid input
	// to the canonicaliser means any second layer applying these same rules
	// disagrees with the first: the gateway accepts a path, a service-side PEP
	// refuses it, or worse, a layer that decodes again sees `/a/b` — two
	// segments and a DIFFERENT OPERATION.
	//
	// Refusing costs the rare path with a literal percent in it (an encoded
	// `50%-complete`, say). That is the correct direction for an input that
	// selects an authorization rule: a refused legitimate request is visible and
	// fixable, a silently divergent one is neither.
	if strings.ContainsRune(decoded, '%') {
		return "", ErrBadEncoding
	}

	segments := strings.Split(decoded, "/")
	out := make([]string, 0, len(segments))
	for _, seg := range segments {
		switch seg {
		case "":
			// Collapse duplicate slashes: "//a" and "/a" are one operation.
			continue
		case ".", "..":
			// REJECTED, not resolved. Resolving decides what the client meant;
			// refusing does not.
			return "", ErrDotSegment
		default:
			out = append(out, seg)
		}
	}

	// "/x" and "/x/" are the same operation, so the canonical form carries no
	// trailing slash. The root is "/" and is the only path that is just a slash.
	if len(out) == 0 {
		return "/", nil
	}
	return "/" + strings.Join(out, "/"), nil
}

// containsFold reports whether s contains sub, comparing ASCII case-insensitively.
//
// Hand-rolled rather than strings.Contains(strings.ToLower(s), sub): ToLower
// allocates a copy of every request path, and this runs before any limit has
// been applied.
func containsFold(s, sub string) bool {
	if len(sub) == 0 || len(s) < len(sub) {
		return false
	}
	for i := 0; i+len(sub) <= len(s); i++ {
		match := true
		for j := range len(sub) {
			c := s[i+j]
			if c >= 'A' && c <= 'Z' {
				c += 'a' - 'A'
			}
			if c != sub[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}
