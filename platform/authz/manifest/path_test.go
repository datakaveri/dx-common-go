package manifest_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
)

// Target design §6.4 makes fuzz coverage for encoded slash, invalid UTF-8, dot
// segments, duplicate slash and long paths a MERGE GATE. The table below is the
// regression corpus; FuzzNormalize is the open-ended half.
//
// The property under all of it: normalization decides WHICH OPERATION a request
// is, and the operation decides whether authentication is required. A path that
// normalizes one way here and another way upstream is a request authorized as
// one thing and executed as another.

func TestNormalize(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
		err  error
		why  string
	}{
		// ── canonical forms ──────────────────────────────────────────────
		{name: "already canonical", in: "/ogc/collections", want: "/ogc/collections"},
		{name: "root", in: "/", want: "/"},
		{
			name: "trailing slash is the same operation", in: "/ogc/items/", want: "/ogc/items",
			why: "/x and /x/ must not be two operations with two policies",
		},
		{
			name: "duplicate slashes collapse", in: "//ogc///items", want: "/ogc/items",
			why: "otherwise //admin is an unlisted path that matches nothing and falls through",
		},
		{
			name: "percent-decoded once", in: "/ogc/my%20collection", want: "/ogc/my collection",
			why: "the matcher compares the decoded form, so it must be produced here",
		},
		{
			name: "plus is NOT a space in a path", in: "/ogc/a+b", want: "/ogc/a+b",
			why: "+ means space in a query string, not in a path; decoding it would make " +
				"/a+b and /a b one operation",
		},

		// ── refusals ─────────────────────────────────────────────────────
		{
			name: "encoded slash", in: "/ogc/a%2Fb", err: manifest.ErrEncodedSlash,
			why: "after decoding, %2F is indistinguishable from a real separator — /a%2Fb is " +
				"one segment and /a/b is two, and they are different operations",
		},
		{
			name: "encoded slash lowercase", in: "/ogc/a%2fb", err: manifest.ErrEncodedSlash,
			why: "case in the escape must not be a bypass",
		},
		{
			name: "double-encoded slash", in: "/ogc/a%252Fb", err: manifest.ErrBadEncoding,
			why: "THE FUZZER FOUND THIS. Decoding once yields the literal text %2F, which is " +
				"not a separator — but that canonical form cannot itself be normalized, so a " +
				"second layer applying these rules would reject a path this one accepted, or " +
				"decode again and see /a/b: two segments and a different operation",
		},
		{
			name: "dot segment", in: "/ogc/./items", err: manifest.ErrDotSegment,
			why: "rejected rather than resolved: resolving decides what the client meant",
		},
		{
			name: "double dot segment", in: "/ogc/../admin", err: manifest.ErrDotSegment,
			why: "normalizing this to /admin would authorize the request as /ogc/... and " +
				"execute it as /admin",
		},
		{
			name: "encoded dot segment", in: "/ogc/%2E%2E/admin", err: manifest.ErrDotSegment,
			why: "the escape must not hide a traversal from the segment check",
		},
		{name: "relative path", in: "ogc/items", err: manifest.ErrNotAbsolute},
		{name: "empty", in: "", err: manifest.ErrNotAbsolute},
		{name: "malformed escape", in: "/ogc/%zz", err: manifest.ErrBadEncoding},
		{name: "truncated escape", in: "/ogc/%2", err: manifest.ErrBadEncoding},
		{
			name: "NUL byte", in: "/ogc/a%00b", err: manifest.ErrControlChar,
			why: "stripping it would make this layer and the upstream disagree about where " +
				"the path ends",
		},
		{name: "newline", in: "/ogc/a%0Ab", err: manifest.ErrControlChar},
		{name: "over the length bound", in: "/" + strings.Repeat("a", 4096), err: manifest.ErrTooLong},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := manifest.Normalize(tt.in)
			if tt.err != nil {
				if !errors.Is(err, tt.err) {
					t.Fatalf("Normalize(%q) = (%q, %v), want error %v — %s",
						tt.in, got, err, tt.err, tt.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("Normalize(%q) = %v, want %q — %s", tt.in, err, tt.want, tt.why)
			}
			if got != tt.want {
				t.Errorf("Normalize(%q) = %q, want %q — %s", tt.in, got, tt.want, tt.why)
			}
		})
	}
}

// TestNormalizeIsIdempotent: the canonical form must already be canonical.
//
// If normalizing twice differed from normalizing once, then whether a request
// matched would depend on how many layers had touched it.
func TestNormalizeIsIdempotent(t *testing.T) {
	for _, in := range []string{
		"/", "/a", "/a/b", "//a//b//", "/a/b/", "/ogc/my%20collection", "/a+b",
	} {
		once, err := manifest.Normalize(in)
		if err != nil {
			t.Fatalf("Normalize(%q): %v", in, err)
		}
		twice, err := manifest.Normalize(once)
		if err != nil {
			t.Fatalf("Normalize(Normalize(%q)) = %v", in, err)
		}
		if once != twice {
			t.Errorf("not idempotent: %q -> %q -> %q", in, once, twice)
		}
	}
}

// FuzzNormalize is the open-ended half of the §6.4 gate.
//
// It asserts INVARIANTS rather than outputs, because the interesting failures
// are the ones nobody thought to tabulate:
//
//   - never panics (this runs on unauthenticated input);
//   - any accepted output is absolute, canonical, and contains no separator
//     ambiguity or traversal — i.e. nothing that would let the matcher and the
//     upstream disagree.
func FuzzNormalize(f *testing.F) {
	for _, seed := range []string{
		"/", "/a", "//a//", "/a/../b", "/a%2Fb", "/a%252Fb", "/%2E%2E/x",
		"/a%00b", "/a\x7f", "/ünïcödé", "/a+b", "/%", "/%zz", "ogc",
		strings.Repeat("/a", 500),
	} {
		f.Add(seed)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := manifest.Normalize(raw)
		if err != nil {
			if got != "" {
				t.Errorf("Normalize(%q) returned BOTH %q and error %v", raw, got, err)
			}
			return
		}

		if got == "" || got[0] != '/' {
			t.Fatalf("Normalize(%q) = %q, which is not absolute", raw, got)
		}
		if strings.Contains(got, "//") {
			t.Errorf("Normalize(%q) = %q still contains a duplicate slash", raw, got)
		}
		if len(got) > 1 && strings.HasSuffix(got, "/") {
			t.Errorf("Normalize(%q) = %q still has a trailing slash", raw, got)
		}
		for _, seg := range strings.Split(strings.TrimPrefix(got, "/"), "/") {
			if seg == "." || seg == ".." {
				t.Errorf("Normalize(%q) = %q retains a dot segment", raw, got)
			}
		}
		for _, r := range got {
			if r < 0x20 || r == 0x7f {
				t.Errorf("Normalize(%q) = %q retains a control character", raw, got)
			}
		}
		// Idempotence must hold for every accepted input, not just the seeds.
		again, err := manifest.Normalize(got)
		if err != nil || again != got {
			t.Errorf("not idempotent: %q -> %q -> (%q, %v)", raw, got, again, err)
		}
	})
}
