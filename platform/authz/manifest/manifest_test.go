package manifest_test

import (
	"errors"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
	"github.com/datakaveri/dx-common-go/platform/authz/vocabulary"
)

// AUTHZ-1's manifest compiler and matcher.
//
// The property that matters most is the boring one: A REQUEST THAT MATCHES NO
// OPERATION IS DENIED. §5.20 #7 makes it normative, and it is normative because
// the alternative — passing an unlisted path through on the theory that it is
// probably harmless — is how an unlisted admin endpoint stays unlisted.

func op(id, method, path string, auth manifest.AuthMode, perm string) manifest.Operation {
	return manifest.Operation{
		OperationID: id, Method: method, Path: path,
		Authentication: auth, Permission: perm,
	}
}

func manifestOf(ops ...manifest.Operation) *manifest.Manifest {
	return &manifest.Manifest{
		APIVersion: manifest.SchemaVersion,
		Service:    "dx-test-go",
		Revision:   "abc123",
		Operations: ops,
	}
}

func mustCompile(t *testing.T, m *manifest.Manifest) *manifest.Compiled {
	t.Helper()
	c, err := manifest.Compile(m)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	return c
}

func TestMatch(t *testing.T) {
	c := mustCompile(t, manifestOf(
		op("listCollections", "GET", "/ogc/collections", manifest.AuthNone, ""),
		op("getCollection", "GET", "/ogc/collections/{collectionId}", manifest.AuthRequired, string(vocabulary.PermRead)),
		op("getItems", "GET", "/ogc/collections/{collectionId}/items", manifest.AuthRequired, string(vocabulary.PermQuery)),
		op("createItem", "POST", "/ogc/collections/{collectionId}/items", manifest.AuthRequired, string(vocabulary.PermWrite)),
	))

	tests := []struct {
		name, method, path, wantOp string
		wantParam                  string
		wantErr                    error
		why                        string
	}{
		{name: "literal", method: "GET", path: "/ogc/collections", wantOp: "listCollections"},
		{
			name: "one parameter", method: "GET", path: "/ogc/collections/aqi",
			wantOp: "getCollection", wantParam: "aqi",
		},
		{
			name: "deeper path is a DIFFERENT operation", method: "GET",
			path: "/ogc/collections/aqi/items", wantOp: "getItems", wantParam: "aqi",
			why: "a prefix match would authorize this as getCollection, which needs only read " +
				"— the items endpoint needs query",
		},
		{
			name: "method selects the operation", method: "POST",
			path: "/ogc/collections/aqi/items", wantOp: "createItem", wantParam: "aqi",
			why: "same path, different permission (write, not query) — matching on path alone " +
				"would authorize a write as a read",
		},
		{
			name: "trailing slash is the same operation", method: "GET",
			path: "/ogc/collections/", wantOp: "listCollections",
		},
		{
			name: "unknown path", method: "GET", path: "/ogc/secret", wantErr: manifest.ErrNoMatch,
			why: "an unmatched request is DENIED; passing it through is how an unlisted " +
				"endpoint stays unlisted",
		},
		{
			name: "unknown method", method: "DELETE", path: "/ogc/collections",
			wantErr: manifest.ErrNoMatch,
			why:     "declaring GET does not declare DELETE",
		},
		{
			name: "extra segment", method: "GET", path: "/ogc/collections/aqi/items/extra",
			wantErr: manifest.ErrNoMatch,
		},
		{
			name: "traversal is refused before matching", method: "GET",
			path: "/ogc/collections/../secret", wantErr: manifest.ErrDotSegment,
			why: "normalization refuses it, so it never reaches the matcher at all",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOp, params, err := c.Match(tt.method, tt.path)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("Match(%s,%s) = (%q,%v), want error %v — %s",
						tt.method, tt.path, gotOp.OperationID, err, tt.wantErr, tt.why)
				}
				return
			}
			if err != nil {
				t.Fatalf("Match(%s,%s) = %v, want %q — %s", tt.method, tt.path, err, tt.wantOp, tt.why)
			}
			if gotOp.OperationID != tt.wantOp {
				t.Errorf("matched %q, want %q — %s", gotOp.OperationID, tt.wantOp, tt.why)
			}
			if tt.wantParam != "" && params["collectionId"] != tt.wantParam {
				t.Errorf("collectionId = %q, want %q", params["collectionId"], tt.wantParam)
			}
		})
	}
}

// TestCompileRejectsAmbiguity is why matching cannot depend on declaration
// order.
//
// Two operations that could both match one request are refused at COMPILE time.
// Resolving that at request time by order would make authorization depend on
// the sequence someone happened to write the endpoints in — and the weaker rule
// winning is a silent downgrade, exactly the P0-16 gateway defect in another
// form.
func TestCompileRejectsAmbiguity(t *testing.T) {
	_, err := manifest.Compile(manifestOf(
		op("byId", "GET", "/items/{id}", manifest.AuthRequired, string(vocabulary.PermRead)),
		op("byName", "GET", "/items/{name}", manifest.AuthNone, ""),
	))
	if !errors.Is(err, manifest.ErrAmbiguous) {
		t.Fatalf("Compile = %v, want ErrAmbiguous — one request could match a required-auth "+
			"operation and a public one, and which won would depend on ordering", err)
	}
}

// TestCompileRejectsInvalidManifests covers everything checked once so it is
// never checked per request.
func TestCompileRejectsInvalidManifests(t *testing.T) {
	tests := []struct {
		name string
		m    *manifest.Manifest
		why  string
	}{
		{
			name: "protected operation with no permission",
			m:    manifestOf(op("x", "GET", "/x", manifest.AuthRequired, "")),
			why: "it would authorize on IDENTITY ALONE — any authenticated caller passes, " +
				"which is authentication masquerading as authorization",
		},
		{
			name: "unrecognised authentication mode",
			m:    manifestOf(op("x", "GET", "/x", manifest.AuthMode("maybe"), "read")),
			why: "no default branch: guessing 'required' breaks the service and guessing " +
				"'none' exposes it, so neither is acceptable",
		},
		{
			name: "duplicate operationId",
			m: manifestOf(
				op("dup", "GET", "/a", manifest.AuthNone, ""),
				op("dup", "GET", "/b", manifest.AuthNone, ""),
			),
			why: "the id is how a decision is attributed in audit",
		},
		{name: "no operationId", m: manifestOf(op("", "GET", "/x", manifest.AuthNone, ""))},
		{name: "no method", m: manifestOf(op("x", "", "/x", manifest.AuthNone, ""))},
		{name: "relative path", m: manifestOf(op("x", "GET", "x", manifest.AuthNone, ""))},
		{
			name: "segment mixing literal and parameter",
			m:    manifestOf(op("x", "GET", "/a{id}b", manifest.AuthNone, "")),
			why:  "the matcher does not parse within a segment, so accepting it would mean the template and the matcher disagree",
		},
		{name: "empty parameter name", m: manifestOf(op("x", "GET", "/a/{}", manifest.AuthNone, ""))},
		{name: "no operations", m: manifestOf()},
		{
			name: "wrong schema version",
			m: &manifest.Manifest{
				APIVersion: "authz.dx/v0", Service: "s",
				Operations: []manifest.Operation{op("x", "GET", "/x", manifest.AuthNone, "")},
			},
		},
		{
			name: "no service",
			m: &manifest.Manifest{
				APIVersion: manifest.SchemaVersion,
				Operations: []manifest.Operation{op("x", "GET", "/x", manifest.AuthNone, "")},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := manifest.Compile(tt.m); err == nil {
				t.Fatalf("Compile accepted an invalid manifest — %s", tt.why)
			}
		})
	}
}

// TestCompileAcceptsDifferentLengthsWithSameShape: two templates of different
// lengths cannot both match, so they are not ambiguous.
func TestCompileAcceptsDifferentLengthsWithSameShape(t *testing.T) {
	if _, err := manifest.Compile(manifestOf(
		op("a", "GET", "/items/{id}", manifest.AuthRequired, "read"),
		op("b", "GET", "/items/{id}/parts", manifest.AuthRequired, "read"),
	)); err != nil {
		t.Fatalf("Compile rejected two unambiguous templates: %v", err)
	}
}

// TestMatchIsOrderIndependent: since ambiguity is impossible after Compile, the
// result cannot depend on declaration order. Asserted directly, because it is
// the property the ambiguity check exists to guarantee.
func TestMatchIsOrderIndependent(t *testing.T) {
	forward := mustCompile(t, manifestOf(
		op("list", "GET", "/items", manifest.AuthNone, ""),
		op("get", "GET", "/items/{id}", manifest.AuthRequired, "read"),
	))
	reversed := mustCompile(t, manifestOf(
		op("get", "GET", "/items/{id}", manifest.AuthRequired, "read"),
		op("list", "GET", "/items", manifest.AuthNone, ""),
	))

	for _, path := range []string{"/items", "/items/abc"} {
		a, _, errA := forward.Match("GET", path)
		b, _, errB := reversed.Match("GET", path)
		if errA != nil || errB != nil {
			t.Fatalf("Match(%q): %v / %v", path, errA, errB)
		}
		if a.OperationID != b.OperationID {
			t.Errorf("Match(%q) depends on declaration order: %q vs %q",
				path, a.OperationID, b.OperationID)
		}
	}
}

// TestPermissionsAreVocabularyPermissions: the manifest carries permission
// STRINGS, so nothing structurally stops a service declaring "api" — the exact
// value that caused the projection defect. This asserts the compiled manifests
// used here parse under the real vocabulary.
func TestPermissionsAreVocabularyPermissions(t *testing.T) {
	c := mustCompile(t, manifestOf(
		op("r", "GET", "/a", manifest.AuthRequired, string(vocabulary.PermRead)),
		op("q", "GET", "/b", manifest.AuthRequired, string(vocabulary.PermQuery)),
		op("pub", "GET", "/c", manifest.AuthNone, ""),
	))
	for _, path := range []string{"/a", "/b"} {
		o, _, err := c.Match("GET", path)
		if err != nil {
			t.Fatalf("Match(%q): %v", path, err)
		}
		if _, perr := vocabulary.ParsePermission(o.Permission); perr != nil {
			t.Errorf("operation %q declares permission %q, which is not in the vocabulary: %v",
				o.OperationID, o.Permission, perr)
		}
	}
}
