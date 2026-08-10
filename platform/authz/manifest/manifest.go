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

	"github.com/datakaveri/dx-common-go/platform/authz/vocabulary"
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

// Authorization is HOW an operation is authorized once the caller is
// authenticated — the question "authenticated as whom, and then what?".
//
// # Why this exists
//
// The manifest originally assumed every protected operation is resource-scoped:
// a permission checked against one resource id. Compile rejected a protected
// operation with no permission because it "would authorize on identity alone",
// and as a DEFAULT that rejection is right — nobody should reach identity-only
// authorization by forgetting to write a permission.
//
// But the dx-catalogue-go pilot found four operations for which authorizing on
// identity alone is CORRECT, and no amount of care would make them fit:
//
//	myAssets / orgAssets / platformAssets — listings that return only the
//	    caller's own assets. There is no single resource to check; the scoping
//	    IS the authorization, and it happens inside the query.
//	approveItem — an admin action gated by a realm role, not by any relation
//	    the caller holds on the item being approved.
//
// Forcing those into `read` and `own` made the annotation look considered when
// it was a placeholder, and a placeholder in a security artifact is worse than
// a gap, because it stops anyone looking again.
//
// # The property that keeps this from being a hole
//
// Every class must be DECLARED. There is no inference from an absent permission
// to AuthzIdentity — that is exactly the mistake Compile was right to reject.
// An operation that states `authentication: required` and nothing else is still
// an error; the difference is that the error now names three choices instead of
// demanding a permission that may not exist.
type Authorization string

const (
	// AuthzResource — a vocabulary permission is checked against one resource.
	// The original and overwhelmingly common case. Inferred when a permission
	// is present, so every existing annotation keeps its meaning exactly.
	AuthzResource Authorization = "resource"

	// AuthzIdentity — the verified identity IS the authorization, and the
	// SERVICE scopes the result to that caller.
	//
	// The gateway enforces authentication and nothing further, which is the
	// honest description of what it can do: "return only rows belonging to the
	// caller" is a property of a query, not of a request, and no PEP outside
	// the service can check it. Declaring this where the service does NOT
	// scope its results is a defect this cannot catch — which is precisely why
	// it must be written down per operation, where it shows up in a spec diff
	// and can be reviewed, rather than inferred from silence.
	AuthzIdentity Authorization = "identity"

	// AuthzRole — the caller must hold one of the declared realm roles.
	//
	// Enforceable at the gateway today: roles come from the validated token.
	// This is for administrative actions where the authority is a property of
	// the caller rather than a relation on the object — approving an item is
	// not something an item's owner may do, so no relation on the item is the
	// right check.
	AuthzRole Authorization = "role"
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

	// Authentication, Authorization, Permission and Roles are the POLICY half.
	Authentication AuthMode `json:"authentication" yaml:"authentication"`
	// Authorization is how the operation is authorized once authenticated.
	// Empty means AuthzResource, which is what every permission-carrying
	// annotation written before this field existed meant.
	Authorization Authorization `json:"authorization,omitempty" yaml:"authorization,omitempty"`
	// Permission is the vocabulary permission checked for this operation.
	// Set only for AuthzResource.
	Permission string `json:"permission,omitempty" yaml:"permission,omitempty"`
	// Roles are the realm roles that may invoke this operation, any one of
	// which suffices. Set only for AuthzRole.
	Roles []string `json:"roles,omitempty" yaml:"roles,omitempty"`

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

		// One validator, called from here AND from FromOpenAPI. AUTHZ-1 found
		// three hand-maintained copies of one relation contract with nothing
		// checking them against each other; two copies of the policy rules
		// would fail the same way, and the one that drifts is whichever is not
		// on the path being tested.
		if problems := ValidatePolicy(op); len(problems) > 0 {
			return nil, fmt.Errorf("%w: operation %q: %s",
				ErrInvalid, op.OperationID, strings.Join(problems, "; "))
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

// ValidatePolicy checks one operation's POLICY half against the rules, and is
// the only place those rules live.
//
// It returns every problem rather than the first: a service annotating forty
// operations should get forty answers from one build.
//
// # The rule that closes the gap without opening one
//
// A protected operation must state HOW it is authorized. Before the
// Authorization field existed the only expressible answer was "a permission on
// a resource", so operations that are authorized by identity or by role were
// annotated with a permission that did not describe them. Now there are three
// answers — and still no fourth answer of "say nothing".
func ValidatePolicy(op Operation) []string {
	var problems []string

	switch op.Authentication {
	case AuthRequired, AuthOptional, AuthNone:
	case "":
		problems = append(problems, "authentication is required")
	default:
		// No default acceptance: an unrecognised mode must not be guessed at,
		// and guessing "required" would be as wrong as guessing "none" — one
		// breaks the service, the other exposes it.
		problems = append(problems, fmt.Sprintf(
			"authentication %q; want required, optional or none", op.Authentication))
	}

	authz := op.Authorization
	if authz == "" && op.Permission != "" {
		// Back-compatible inference, and the ONLY inference there is: an
		// annotation that names a permission always meant a resource check.
		// Nothing is inferred from silence.
		authz = AuthzResource
	}

	if op.Authentication == AuthNone {
		if op.Authorization != "" {
			problems = append(problems, fmt.Sprintf(
				"authentication is none but authorization %q is declared — a public operation "+
					"authorizes nobody", op.Authorization))
		}
		if op.Permission != "" {
			problems = append(problems, fmt.Sprintf(
				"authentication is none but permission %q is declared — one of the two is wrong, "+
					"and guessing which would be guessing whether the operation is public",
				op.Permission))
		}
		if len(op.Roles) > 0 {
			problems = append(problems, "authentication is none but roles are declared")
		}
		return append(problems, validateShape(op)...)
	}

	switch authz {
	case AuthzResource:
		if op.Permission == "" {
			problems = append(problems, fmt.Sprintf(
				"authentication %q with a resource check but no permission", op.Authentication))
			break
		}
		// THE CHECK THAT STOPS accessType COMING BACK. A service could
		// otherwise declare `permission: api` and the gateway would faithfully
		// check a relation the model does not define — the defect AUTHZ-1
		// closed on the projection side, re-entering through a spec.
		if _, err := vocabulary.ParsePermission(op.Permission); err != nil {
			problems = append(problems, fmt.Sprintf(
				"permission %q is not in the ratified vocabulary (%v) — a service may not invent one",
				op.Permission, vocabulary.Permissions()))
		}
		if len(op.Roles) > 0 {
			problems = append(problems, "roles are declared but authorization is by resource permission")
		}

	case AuthzIdentity:
		// The gateway enforces authentication and stops. Requiring
		// `authentication: required` is not pedantry: with `optional`, an
		// anonymous caller would pass a check whose entire content is "the
		// caller is someone".
		if op.Authentication != AuthRequired {
			problems = append(problems, fmt.Sprintf(
				"authorization is by identity but authentication is %q — an anonymous caller would "+
					"pass a check whose whole content is that there is a caller", op.Authentication))
		}
		if op.Permission != "" {
			problems = append(problems, fmt.Sprintf(
				"authorization is by identity but permission %q is declared — identity scoping "+
					"happens inside the service's query, where no permission is checked",
				op.Permission))
		}
		if len(op.Roles) > 0 {
			problems = append(problems, "authorization is by identity but roles are declared")
		}

	case AuthzRole:
		if op.Authentication != AuthRequired {
			problems = append(problems, fmt.Sprintf(
				"authorization is by role but authentication is %q — a role cannot be read from a "+
					"token that need not be present", op.Authentication))
		}
		if len(op.Roles) == 0 {
			problems = append(problems, "authorization is by role but no roles are declared — "+
				"the check would admit every authenticated caller")
		}
		for _, role := range op.Roles {
			if strings.TrimSpace(role) == "" {
				problems = append(problems, "an empty role is declared")
			}
		}
		if op.Permission != "" {
			problems = append(problems, fmt.Sprintf(
				"authorization is by role but permission %q is declared", op.Permission))
		}

	case "":
		// The gap-closing message. It replaces "requires authentication but
		// names no permission", which demanded something that for some
		// operations does not exist and so invited a placeholder.
		problems = append(problems, fmt.Sprintf(
			"authentication %q but no authorization — declare one of: "+
				"`resource` with a permission, `identity` where the service scopes results to the "+
				"caller, or `role` with the realm roles allowed", op.Authentication))

	default:
		problems = append(problems, fmt.Sprintf(
			"authorization %q; want resource, identity or role", op.Authorization))
	}

	return append(problems, validateShape(op)...)
}

// validateShape checks the SHAPE half — the part the service owns.
func validateShape(op Operation) []string {
	if op.Resource.IDFrom == "" {
		return nil
	}
	if err := validateIDFrom(op.Resource.IDFrom, op.Path); err != nil {
		return []string{fmt.Sprintf("resource.idFrom: %v", err)}
	}
	return nil
}

// EffectiveAuthorization is the operation's authorization class after the one
// inference the format allows.
//
// Callers MUST use this rather than reading the field: an annotation written
// before the field existed carries a permission and an empty class, and reading
// the raw field would make it look like an unauthorized operation.
func (o Operation) EffectiveAuthorization() Authorization {
	if o.Authorization == "" && o.Permission != "" {
		return AuthzResource
	}
	return o.Authorization
}
