package manifest

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

// Compiling a service's OpenAPI into a manifest (target design §3.2.1–3.2.2).
//
// # The rule that makes this worth having
//
// MISSING x-dx-authz IS A BUILD FAILURE. There is no implicit public default and
// no "we will add it later" state: an operation that does not declare its policy
// cannot be compiled, so it cannot be deployed, so it cannot be reached
// unlisted. The design states it as "missing x-dx-authz fails CI and fails
// closed at runtime"; this is the CI half, and the matcher's ErrNoMatch is the
// runtime half.
//
// The alternative — defaulting an undeclared operation to `required` — sounds
// safe and is not. It would let a service ship endpoints nobody reviewed, and
// they would work, because most endpoints do want authentication. The gap would
// only surface on the one endpoint that needed something else.
//
// # Permissions are validated against the ratified vocabulary
//
// The design's own example writes `permission: collection.items.read`, a dotted
// per-service string. AUTHZ-PERMISSION-REGISTRY.md supersedes that: there are
// five permissions and a service may not invent a sixth. Validating here is what
// stops the `accessType` class of defect re-entering through a service's spec —
// a service could otherwise declare `permission: api` and the gateway would
// faithfully check a relation that does not exist.

// extensionKey is the OpenAPI extension a service declares its policy under.
const extensionKey = "x-dx-authz"

// authzExtension is the declared shape.
//
// Fields the platform does not yet act on (decisionProfile, context,
// enforcement) are parsed and CARRIED rather than dropped: a manifest that
// silently discards a service's declaration would let an operator believe a
// control is configured when the compiled artifact does not contain it.
type authzExtension struct {
	Authentication  string   `json:"authentication"`
	Authorization   string   `json:"authorization,omitempty"`
	Permission      string   `json:"permission,omitempty"`
	Roles           []string `json:"roles,omitempty"`
	Workloads       []string `json:"workloads,omitempty"`
	DecisionProfile string   `json:"decisionProfile,omitempty"`
	Resource        *struct {
		Type   string `json:"type"`
		IDFrom string `json:"idFrom,omitempty"`
	} `json:"resource,omitempty"`
}

// CompileOptions carries what the spec itself cannot supply.
type CompileOptions struct {
	// Service is the service identity. Not taken from OpenAPI's info.title,
	// which is a human label and drifts; the manifest's service name is matched
	// against deployment identity and has to be exact.
	Service string
	// Revision is the build's Git revision, recorded so a decision can be
	// attributed to the manifest that produced it.
	Revision string
}

// FromOpenAPI compiles a service's OpenAPI document into a manifest.
//
// It reports EVERY problem it finds rather than the first. A service adding
// annotations to forty operations should get forty answers from one build, not
// forty builds.
func FromOpenAPI(doc *openapi3.T, opts CompileOptions) (*Manifest, error) {
	if doc == nil {
		return nil, fmt.Errorf("%w: nil OpenAPI document", ErrInvalid)
	}
	if opts.Service == "" {
		return nil, fmt.Errorf("%w: CompileOptions.Service is required", ErrInvalid)
	}

	var (
		ops      []Operation
		problems []string
	)

	// Paths are walked in sorted order so the compiled artifact is BYTE-STABLE
	// across builds. An artifact that reorders itself produces a different
	// digest for identical policy, and §5.20 #6 promotes it by digest.
	paths := doc.Paths.Map()
	pathKeys := make([]string, 0, len(paths))
	for p := range paths {
		pathKeys = append(pathKeys, p)
	}
	sort.Strings(pathKeys)

	for _, path := range pathKeys {
		item := paths[path]
		if item == nil {
			continue
		}
		methods := item.Operations()
		methodKeys := make([]string, 0, len(methods))
		for m := range methods {
			methodKeys = append(methodKeys, m)
		}
		sort.Strings(methodKeys)

		for _, method := range methodKeys {
			oaOp := methods[method]
			where := method + " " + path

			raw, ok := oaOp.Extensions[extensionKey]
			if !ok {
				problems = append(problems, fmt.Sprintf(
					"%s declares no %s — every operation must state its authentication and "+
						"permission; there is no public default", where, extensionKey))
				continue
			}

			ext, err := decodeExtension(raw)
			if err != nil {
				problems = append(problems, fmt.Sprintf("%s: %s: %v", where, extensionKey, err))
				continue
			}

			opID := oaOp.OperationID
			if opID == "" {
				problems = append(problems, fmt.Sprintf(
					"%s has no operationId — it is how a decision is attributed in audit", where))
				continue
			}

			op := Operation{
				OperationID:    opID,
				Method:         method,
				Path:           path,
				Authentication: AuthMode(ext.Authentication),
				Authorization:  Authorization(ext.Authorization),
				Permission:     ext.Permission,
				Roles:          ext.Roles,
				Workloads:      ext.Workloads,
			}
			if ext.Resource != nil {
				op.Resource = ResourceRef{Type: ext.Resource.Type, IDFrom: ext.Resource.IDFrom}
			}

			if errs := ValidatePolicy(op); len(errs) > 0 {
				for _, e := range errs {
					problems = append(problems, where+": "+e)
				}
				continue
			}
			ops = append(ops, op)
		}
	}

	if len(problems) > 0 {
		return nil, fmt.Errorf("%w: %d operation(s) rejected:\n  - %s",
			ErrInvalid, len(problems), strings.Join(problems, "\n  - "))
	}

	m := &Manifest{
		APIVersion: SchemaVersion,
		Service:    opts.Service,
		Revision:   opts.Revision,
		Operations: ops,
	}
	// Compile it here so FromOpenAPI cannot return a manifest that Compile
	// would reject — ambiguity in particular, which is a property of the SET
	// and cannot be seen while validating one operation at a time.
	if _, err := Compile(m); err != nil {
		return nil, err
	}
	return m, nil
}

// decodeExtension reads the extension value.
//
// kin-openapi surfaces extensions as `any` holding decoded JSON, so this
// round-trips through JSON rather than type-asserting a map — which would have
// to reimplement the decoding for every field and would silently ignore a
// misspelled key.
func decodeExtension(raw any) (*authzExtension, error) {
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, fmt.Errorf("cannot re-encode: %w", err)
	}
	var ext authzExtension
	dec := json.NewDecoder(strings.NewReader(string(b)))
	// Unknown fields are an ERROR, not noise. A misspelled `authentcation`
	// would otherwise leave the mode empty and be reported as "unrecognised
	// mode" — true, but unhelpful, and the real fault is one character away.
	dec.DisallowUnknownFields()
	if err := dec.Decode(&ext); err != nil {
		return nil, err
	}
	return &ext, nil
}

// validateIDFrom checks the extraction locator, including that a path parameter
// actually exists in the template.
//
// A locator naming a parameter the path does not have would extract nothing at
// request time, and "nothing" is the resource id the check then runs against —
// so this is a build-time failure rather than a runtime denial nobody can
// explain.
func validateIDFrom(idFrom, path string) error {
	kind, name, ok := strings.Cut(idFrom, ".")
	if !ok || name == "" {
		return fmt.Errorf("%q must be path.<param>, query.<name> or header.<Name>", idFrom)
	}
	switch kind {
	case "query", "header":
		return nil
	case "path":
		if !strings.Contains(path, "{"+name+"}") {
			return fmt.Errorf("names path parameter %q, which %q does not contain", name, path)
		}
		return nil
	default:
		return fmt.Errorf("%q has unknown source %q; want path, query or header", idFrom, kind)
	}
}
