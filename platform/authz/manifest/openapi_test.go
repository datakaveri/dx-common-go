package manifest_test

import (
	"strings"
	"testing"

	"github.com/getkin/kin-openapi/openapi3"

	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
)

// Compiling OpenAPI into a manifest (AUTHZ-1).
//
// The rule everything else follows from: MISSING x-dx-authz IS A BUILD FAILURE.
// There is no implicit public default, so an operation nobody reviewed cannot
// reach production unlisted.
//
// Defaulting an undeclared operation to `required` would sound safe and would
// not be: it would let a service ship endpoints nobody looked at, and they would
// mostly work, so the gap would surface only on the one endpoint that needed
// something else.

func specOf(t *testing.T, yaml string) *openapi3.T {
	t.Helper()
	doc, err := openapi3.NewLoader().LoadFromData([]byte(yaml))
	if err != nil {
		t.Fatalf("load spec: %v", err)
	}
	return doc
}

const specHeader = `openapi: 3.0.3
info: {title: test, version: "1"}
paths:
`

func compile(t *testing.T, body string) (*manifest.Manifest, error) {
	t.Helper()
	return manifest.FromOpenAPI(specOf(t, specHeader+body),
		manifest.CompileOptions{Service: "dx-test-go", Revision: "abc123"})
}

func TestFromOpenAPI(t *testing.T) {
	m, err := compile(t, `
  /ogc/collections:
    get:
      operationId: listCollections
      x-dx-authz: {authentication: none}
      responses: {"200": {description: ok}}
  /ogc/collections/{collectionId}/items:
    get:
      operationId: getItems
      parameters:
        - {name: collectionId, in: path, required: true, schema: {type: string}}
      x-dx-authz:
        authentication: required
        permission: query
        resource: {type: resource, idFrom: path.collectionId}
      responses: {"200": {description: ok}}
`)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if m.Service != "dx-test-go" || m.Revision != "abc123" {
		t.Errorf("identity not carried: %+v", m)
	}
	if len(m.Operations) != 2 {
		t.Fatalf("compiled %d operations, want 2", len(m.Operations))
	}

	// And it must actually match.
	c, err := manifest.Compile(m)
	if err != nil {
		t.Fatalf("Compile: %v", err)
	}
	op, params, err := c.Match("GET", "/ogc/collections/aqi/items")
	if err != nil {
		t.Fatalf("Match: %v", err)
	}
	if op.OperationID != "getItems" || params["collectionId"] != "aqi" {
		t.Errorf("matched %+v params=%v", op, params)
	}
}

// TestMissingExtensionIsABuildFailure is the headline rule.
func TestMissingExtensionIsABuildFailure(t *testing.T) {
	_, err := compile(t, `
  /secret:
    get:
      operationId: getSecret
      responses: {"200": {description: ok}}
`)
	if err == nil {
		t.Fatal("an operation with no x-dx-authz compiled — it would ship unreviewed, and " +
			"whether it is reachable would depend on nobody noticing")
	}
	if !strings.Contains(err.Error(), "x-dx-authz") {
		t.Errorf("error does not name the missing extension: %v", err)
	}
}

// TestPermissionMustBeInTheVocabulary is what stops accessType coming back.
//
// A service could otherwise declare `permission: api` and the gateway would
// faithfully check a relation the model does not define — the projection defect
// re-entering through a spec instead of through code.
func TestPermissionMustBeInTheVocabulary(t *testing.T) {
	for _, bad := range []string{"api", "file", "sub", "viewer", "editor", "collection.items.read"} {
		t.Run(bad, func(t *testing.T) {
			_, err := compile(t, `
  /x:
    get:
      operationId: getX
      x-dx-authz: {authentication: required, permission: `+bad+`}
      responses: {"200": {description: ok}}
`)
			if err == nil {
				t.Fatalf("permission %q compiled — a service may not invent one", bad)
			}
		})
	}
}

func TestRejectedDeclarations(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
		why  string
	}{
		{
			// Still rejected — silence never authorizes. What changed is the
			// message: it used to demand a permission, which for an
			// identity-scoped listing does not exist, so the only way to
			// satisfy it was to invent one. Naming all three classes is what
			// stops the next author reaching for a placeholder.
			name: "required with no authorization at all",
			body: `
  /x:
    get:
      operationId: getX
      x-dx-authz: {authentication: required}
      responses: {"200": {description: ok}}`,
			want: "declare one of",
			why:  "any authenticated caller would pass — authentication masquerading as authorization",
		},
		{
			name: "none WITH a permission",
			body: `
  /x:
    get:
      operationId: getX
      x-dx-authz: {authentication: none, permission: read}
      responses: {"200": {description: ok}}`,
			want: "one of the two is wrong",
			why:  "guessing which would be guessing whether the operation is public",
		},
		{
			name: "unrecognised authentication",
			body: `
  /x:
    get:
      operationId: getX
      x-dx-authz: {authentication: maybe, permission: read}
      responses: {"200": {description: ok}}`,
			want: "want required, optional or none",
		},
		{
			name: "misspelled key",
			body: `
  /x:
    get:
      operationId: getX
      x-dx-authz: {authentcation: required, permission: read}
      responses: {"200": {description: ok}}`,
			want: "unknown field",
			why: "a misspelling would otherwise leave the mode empty and be reported as an " +
				"unrecognised mode — true, but the real fault is one character away",
		},
		{
			name: "no operationId",
			body: `
  /x:
    get:
      x-dx-authz: {authentication: none}
      responses: {"200": {description: ok}}`,
			want: "operationId",
			why:  "it is how a decision is attributed in audit",
		},
		{
			name: "idFrom names a parameter the path lacks",
			body: `
  /x/{id}:
    get:
      operationId: getX
      parameters:
        - {name: id, in: path, required: true, schema: {type: string}}
      x-dx-authz:
        authentication: required
        permission: read
        resource: {type: resource, idFrom: path.collectionId}
      responses: {"200": {description: ok}}`,
			want: "does not contain",
			why: "it would extract nothing at runtime, and nothing is the resource id the check " +
				"then runs against",
		},
		{
			name: "unknown idFrom source",
			body: `
  /x:
    get:
      operationId: getX
      x-dx-authz:
        authentication: required
        permission: read
        resource: {type: resource, idFrom: body.id}
      responses: {"200": {description: ok}}`,
			want: "want path, query or header",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := compile(t, tt.body)
			if err == nil {
				t.Fatalf("compiled an invalid declaration — %s", tt.why)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %q", err, tt.want)
			}
		})
	}
}

// TestAllProblemsAreReportedAtOnce: a service annotating forty operations should
// get forty answers from one build, not forty builds.
func TestAllProblemsAreReportedAtOnce(t *testing.T) {
	_, err := compile(t, `
  /a:
    get:
      operationId: getA
      responses: {"200": {description: ok}}
  /b:
    get:
      operationId: getB
      responses: {"200": {description: ok}}
  /c:
    get:
      operationId: getC
      x-dx-authz: {authentication: required}
      responses: {"200": {description: ok}}
`)
	if err == nil {
		t.Fatal("expected failures")
	}
	for _, want := range []string{"GET /a", "GET /b", "GET /c"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error does not report %s:\n%v", want, err)
		}
	}
	if !strings.Contains(err.Error(), "3 operation(s) rejected") {
		t.Errorf("error does not count the problems:\n%v", err)
	}
}

// TestOutputIsByteStable: §5.20 #6 promotes the artifact by DIGEST, so a
// manifest that reorders itself between builds produces a different digest for
// identical policy and looks like a policy change.
func TestOutputIsByteStable(t *testing.T) {
	body := `
  /z:
    get:
      operationId: getZ
      x-dx-authz: {authentication: none}
      responses: {"200": {description: ok}}
  /a:
    post:
      operationId: postA
      x-dx-authz: {authentication: required, permission: write}
      responses: {"200": {description: ok}}
    get:
      operationId: getA
      x-dx-authz: {authentication: required, permission: read}
      responses: {"200": {description: ok}}
`
	first, err := compile(t, body)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	for range 5 {
		again, err := compile(t, body)
		if err != nil {
			t.Fatalf("compile: %v", err)
		}
		if len(again.Operations) != len(first.Operations) {
			t.Fatalf("operation count varies between builds")
		}
		for i := range first.Operations {
			if first.Operations[i].OperationID != again.Operations[i].OperationID {
				t.Fatalf("operation ORDER varies between builds: %q vs %q at %d — the artifact "+
					"is promoted by digest, so this looks like a policy change",
					first.Operations[i].OperationID, again.Operations[i].OperationID, i)
			}
		}
	}
}

// TestAmbiguityIsCaughtAtCompile: ambiguity is a property of the SET, so it
// cannot be seen while validating one operation. FromOpenAPI runs Compile before
// returning precisely so it cannot hand back a manifest Compile would reject.
func TestAmbiguityIsCaughtAtCompile(t *testing.T) {
	_, err := compile(t, `
  /items/{id}:
    get:
      operationId: byId
      parameters: [{name: id, in: path, required: true, schema: {type: string}}]
      x-dx-authz: {authentication: required, permission: read}
      responses: {"200": {description: ok}}
  /items/{name}:
    get:
      operationId: byName
      parameters: [{name: name, in: path, required: true, schema: {type: string}}]
      x-dx-authz: {authentication: none}
      responses: {"200": {description: ok}}
`)
	if err == nil {
		t.Fatal("two overlapping templates compiled — one request could match a protected " +
			"operation and a public one")
	}
}
