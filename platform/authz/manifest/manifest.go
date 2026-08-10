// Package manifest compiles and matches service-owned operation policy.
//
// # What a manifest is, and who owns which half (AUTHZ-1 / AUTHZ-2)
//
// A service declares its OPERATIONS — method, path template, and where the
// resource id lives. The operator declares POLICY — whether authentication is
// required and which permission is checked. The service is authoritative about
// itself; it is NOT authoritative about how far it is trusted. Reversed, the
// gateway would take its enforcement rules from the thing it polices, and a
// compromised or simply careless service could switch off its own PEP.
//
// That split is why the manifest is compiled at BUILD time from the service's
// OpenAPI and shipped as an immutable artifact (§5.20 #6), rather than fetched
// from the service at request time. A security artifact resolved at runtime from
// a mutable peer is the same class of risk as H-01/H-02 in the review.
//
// # Fail closed is the default, not a mode
//
// `defaults.unmatched: deny` and "missing x-dx-authz fails CI and fails closed
// at runtime" are both normative. A request that matches no operation is denied,
// never passed through on the theory that an unlisted path is probably
// harmless — that theory is how an unlisted admin endpoint stays unlisted.
package manifest

import (
	"errors"
	"fmt"
	"strings"
)

// SchemaVersion is the manifest format this build understands.
//
// The gateway must support the current AND the immediately previous version
// simultaneously (§5.20 #7), because a rolling update always runs two builds at
// once. A loader that accepts only one version turns every manifest change into
// an outage window.
const SchemaVersion = "authz.dx/v1alpha1"

// AuthMode is the authentication requirement for an operation.
type AuthMode string

const (
	// AuthRequired — a verified subject is mandatory.
	AuthRequired AuthMode = "required"
	// AuthOptional — proceed either way; the operation's own logic decides what
	// an anonymous caller may see. Used where public and private variants share
	// a path and only the service can tell them apart.
	AuthOptional AuthMode = "optional"
	// AuthNone — explicitly public. There is NO implicit public default: an
	// operation reaches this only by declaring it.
	AuthNone AuthMode = "none"
)

// Errors surfaced by Compile and Match.
var (
	// ErrNoMatch is returned when no operation matches. Callers MUST treat it
	// as a denial (§5.20 #7: "an unmatched protected operation MUST fail
	// closed"), never as permission to pass the request through.
	ErrNoMatch = errors.New("manifest: no operation matches")
	// ErrAmbiguous is two operations matching one request. Rejected at compile
	// time, so it can never be resolved at request time by ordering — which
	// would make authorization depend on declaration order.
	ErrAmbiguous = errors.New("manifest: ambiguous operation")
	// ErrInvalid is a structurally unusable manifest.
	ErrInvalid = errors.New("manifest: invalid")
)

// ResourceRef says where an operation's resource id is found.
//
// This is the SHAPE half — a fact about the service's own API, which is why the
// service owns it.
type ResourceRef struct {
	// Type is the resource type for the authorization check. Under the ratified
	// registry this is always vocabulary.ResourceType; the field exists so a
	// future per-kind split does not change the manifest format.
	Type string `json:"type" yaml:"type"`
	// IDFrom names the location, as "path.<param>", "query.<name>" or
	// "header.<Name>". Empty means the operation is not resource-scoped.
	IDFrom string `json:"idFrom,omitempty" yaml:"idFrom,omitempty"`
}

// Operation is one declared, policy-carrying endpoint.
type Operation struct {
	OperationID string `json:"operationId" yaml:"operationId"`
	Method      string `json:"method" yaml:"method"`
	// Path is the template, e.g. /ogc/collections/{collectionId}/items.
	Path string `json:"path" yaml:"path"`

	// Authentication and Permission are the POLICY half.
	Authentication AuthMode `json:"authentication" yaml:"authentication"`
	// Permission is the vocabulary permission checked for this operation.
	// Empty is legitimate only when Authentication is none.
	Permission string `json:"permission,omitempty" yaml:"permission,omitempty"`

	Resource ResourceRef `json:"resource,omitzero" yaml:"resource,omitempty"`

	// segments is the compiled path template. Unexported so a caller cannot
	// construct an Operation that matches differently from how it compiled.
	segments []segment
}

// segment is one compiled path component.
type segment struct {
	literal string // set when param == ""
	param   string // set for {param}
}

// Manifest is one service's compiled operation policy.
type Manifest struct {
	APIVersion string `json:"apiVersion" yaml:"apiVersion"`
	Service    string `json:"service" yaml:"service"`
	Revision   string `json:"revision" yaml:"revision"`
	// Operations, in declaration order. Order does NOT affect matching — see
	// Compile's ambiguity rejection.
	Operations []Operation `json:"operations" yaml:"operations"`
}

// Compiled is a validated manifest ready to match against.
//
// A separate type from Manifest on purpose: holding one is proof that
// validation ran. A function taking *Manifest would accept a hand-built value
// that never passed the ambiguity check.
type Compiled struct {
	service string
	// byMethod keys operations by uppercase method, so a request touches only
	// the operations that could possibly match it.
	byMethod map[string][]Operation
}

// Service returns the manifest's service name, for logging and metrics.
func (c *Compiled) Service() string { return c.service }

// Compile validates a manifest and prepares it for matching.
//
// Everything that can be checked once is checked HERE rather than per request:
// a malformed template, a missing permission on a protected operation, and —
// most importantly — two operations that could both match one request.
func Compile(m *Manifest) (*Compiled, error) {
	if m == nil {
		return nil, fmt.Errorf("%w: nil manifest", ErrInvalid)
	}
	if m.APIVersion != SchemaVersion {
		return nil, fmt.Errorf("%w: apiVersion %q, want %q", ErrInvalid, m.APIVersion, SchemaVersion)
	}
	if m.Service == "" {
		return nil, fmt.Errorf("%w: manifest names no service", ErrInvalid)
	}
	if len(m.Operations) == 0 {
		return nil, fmt.Errorf("%w: manifest declares no operations", ErrInvalid)
	}

	out := &Compiled{service: m.Service, byMethod: map[string][]Operation{}}
	seenIDs := map[string]bool{}

	for i := range m.Operations {
		op := m.Operations[i]
		if op.OperationID == "" {
			return nil, fmt.Errorf("%w: operation %d has no operationId", ErrInvalid, i)
		}
		if seenIDs[op.OperationID] {
			return nil, fmt.Errorf("%w: duplicate operationId %q", ErrInvalid, op.OperationID)
		}
		seenIDs[op.OperationID] = true

		switch op.Authentication {
		case AuthRequired, AuthOptional, AuthNone:
		default:
			// No default branch: an operation whose authentication mode is
			// unrecognised must not be guessed at, and guessing "required"
			// would be as wrong as guessing "none" — one breaks the service,
			// the other exposes it.
			return nil, fmt.Errorf("%w: operation %q has authentication %q; want required, optional or none",
				ErrInvalid, op.OperationID, op.Authentication)
		}

		// A protected operation with no permission cannot be checked against
		// anything, so it would silently authorize on authentication alone.
		if op.Authentication == AuthRequired && op.Permission == "" {
			return nil, fmt.Errorf("%w: operation %q requires authentication but names no permission — "+
				"it would authorize on identity alone", ErrInvalid, op.OperationID)
		}

		method := strings.ToUpper(op.Method)
		if method == "" {
			return nil, fmt.Errorf("%w: operation %q has no method", ErrInvalid, op.OperationID)
		}
		segs, err := compilePath(op.Path)
		if err != nil {
			return nil, fmt.Errorf("%w: operation %q: %v", ErrInvalid, op.OperationID, err)
		}
		op.Method = method
		op.segments = segs

		// Ambiguity is rejected at COMPILE time. Resolving it at request time
		// by declaration order would make authorization depend on the order
		// someone happened to write the endpoints in.
		for _, existing := range out.byMethod[method] {
			if templatesOverlap(existing.segments, segs) {
				return nil, fmt.Errorf("%w: %q and %q can both match one request",
					ErrAmbiguous, existing.OperationID, op.OperationID)
			}
		}
		out.byMethod[method] = append(out.byMethod[method], op)
	}
	return out, nil
}

// compilePath turns a template into segments.
func compilePath(tmpl string) ([]segment, error) {
	if tmpl == "" || tmpl[0] != '/' {
		return nil, errors.New("path must be absolute")
	}
	var segs []segment
	for _, raw := range strings.Split(strings.Trim(tmpl, "/"), "/") {
		if raw == "" {
			continue
		}
		if strings.HasPrefix(raw, "{") && strings.HasSuffix(raw, "}") {
			name := raw[1 : len(raw)-1]
			if name == "" {
				return nil, errors.New("empty path parameter")
			}
			segs = append(segs, segment{param: name})
			continue
		}
		if strings.ContainsAny(raw, "{}") {
			// A partial template like /a{id}b would need per-segment parsing
			// the matcher does not do, and accepting it here would mean the
			// template and the matcher disagree.
			return nil, fmt.Errorf("segment %q mixes literal and parameter", raw)
		}
		segs = append(segs, segment{literal: raw})
	}
	return segs, nil
}

// templatesOverlap reports whether two templates could match one request.
//
// Same length, and every position compatible — a parameter is compatible with
// anything, two literals only with each other.
func templatesOverlap(a, b []segment) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].param != "" || b[i].param != "" {
			continue
		}
		if a[i].literal != b[i].literal {
			return false
		}
	}
	return true
}

// Match resolves a request to its operation and path parameters.
//
// It returns ErrNoMatch when nothing matches, and the caller MUST deny — that
// is §5.20 #7's "an unmatched protected operation MUST fail closed", and it is
// the whole reason `defaults.unmatched: deny` is not configurable.
func (c *Compiled) Match(method, rawPath string) (Operation, map[string]string, error) {
	path, err := Normalize(rawPath)
	if err != nil {
		// A path that cannot be normalized is not matched against anything.
		// Refusing here rather than falling back to the raw form is what stops
		// an encoding trick reaching the matcher at all.
		return Operation{}, nil, err
	}

	var reqSegs []string
	if path != "/" {
		reqSegs = strings.Split(strings.TrimPrefix(path, "/"), "/")
	}

	for _, op := range c.byMethod[strings.ToUpper(method)] {
		if len(op.segments) != len(reqSegs) {
			continue
		}
		params := map[string]string{}
		ok := true
		for i, seg := range op.segments {
			if seg.param != "" {
				params[seg.param] = reqSegs[i]
				continue
			}
			if seg.literal != reqSegs[i] {
				ok = false
				break
			}
		}
		if ok {
			return op, params, nil
		}
	}
	return Operation{}, nil, ErrNoMatch
}
