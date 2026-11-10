// Package vocabulary is the ONE place that maps a permission onto an OpenFGA
// relation.
//
// # Why this package exists (ROADMAP AUTHZ-0/AUTHZ-1, target design §5.20 #2)
//
// dx-acl-go projected policy into OpenFGA by writing
// `resource_type = lower(itemType)` and `relation = accessType`, producing
// tuples like `databank` / `api`. The pinned model accepts only the type
// `resource`, so EVERY tuple failed to typecheck and policy written through the
// ACL never reached the PDP. The projection was not merely wrong, it was inert.
//
// The cause was not a typo. Two orthogonal axes had been collapsed into one
// field:
//
//	accessType (api|file|sub) answers HOW data is delivered
//	an FGA relation           answers WHAT the subject may do
//
// No mapping function can be written between those, which is why the fix was
// blocked on a decision rather than on effort. AUTHZ-PERMISSION-REGISTRY.md
// separates them; this package is the executable half of that document.
//
// # Total, by construction
//
// Every function here returns a value or an explicit error. There is NO default
// branch anywhere, and that is deliberate: a default is precisely how
// `accessType` ended up in the relation slot. An unrecognised permission must
// fail loudly at the projection boundary, never become a plausible-looking
// tuple that silently grants or denies the wrong thing.
//
// # Used on both sides of the boundary
//
// dx-acl-go calls it to BUILD tuples; dx-authz-go calls it to RE-VALIDATE what
// it receives before writing to the store. Neither trusts the other's mapping —
// the projection is asynchronous and crosses a queue, so a producer running
// older code must not be able to write a tuple this vocabulary would reject.
package vocabulary

import "fmt"

// ResourceType is the ONLY FGA type a permission is ever granted on.
//
// One generic type is a ratified decision (registry §1): the domain kind —
// databank, dataset, collection, agent — is catalogue metadata, not an FGA
// type. It is a constant rather than a parameter so that the original defect,
// deriving a type from an item's kind, cannot be reintroduced by a caller.
const ResourceType = "resource"

// Permission is what a subject may do with a resource.
//
// Five, ordered by strength. See the registry for the full definitions; the
// short version is that `read` is "this exists", `query` is "its contents", and
// the split between them is the one a data exchange charges for.
type Permission string

const (
	// PermRead — the resource exists and its metadata is visible.
	PermRead Permission = "read"
	// PermQuery — the resource's CONTENTS may be accessed.
	PermQuery Permission = "query"
	// PermWrite — the resource may be mutated.
	PermWrite Permission = "write"
	// PermShare — access may be granted to others, but not ownership, and the
	// resource cannot be destroyed.
	PermShare Permission = "share"
	// PermOwn — full control, including deletion.
	PermOwn Permission = "own"
)

// SubjectKind is what kind of thing holds a permission.
type SubjectKind string

const (
	SubjectUser         SubjectKind = "user"
	SubjectOrganization SubjectKind = "organization"
	SubjectGroup        SubjectKind = "group"
	// SubjectAgent holds only delegated_* relations, never a user's own. The
	// dual check at the PEP requires both the user's relation and the agent's
	// delegated twin, so an agent can never exceed the user it acts for.
	SubjectAgent SubjectKind = "agent"
)

// Errors. Distinct values so a caller can tell a bad input from a forbidden
// combination, and so tests assert on the cause rather than on message text.
var (
	// ErrUnknownPermission is an input outside the five-permission vocabulary.
	ErrUnknownPermission = fmt.Errorf("vocabulary: unknown permission")
	// ErrUnknownSubjectKind is an input outside the four subject kinds.
	ErrUnknownSubjectKind = fmt.Errorf("vocabulary: unknown subject kind")
	// ErrPermissionNotGrantable is a valid permission that this subject kind
	// may not hold.
	ErrPermissionNotGrantable = fmt.Errorf("vocabulary: permission not grantable to this subject kind")
)

// Permissions returns the vocabulary, weakest first.
//
// Exported so tests can be exhaustive over it rather than restating the list —
// a test that enumerates its own copy stops covering the vocabulary the moment
// someone adds to it.
func Permissions() []Permission {
	return []Permission{PermRead, PermQuery, PermWrite, PermShare, PermOwn}
}

// SubjectKinds returns every subject kind. Same reason as Permissions.
func SubjectKinds() []SubjectKind {
	return []SubjectKind{SubjectUser, SubjectOrganization, SubjectGroup, SubjectAgent}
}

// Implies reports whether holding p also confers q.
//
// The chain is own → share → write → query → read, each implying everything to
// its right. It is expressed in the FGA model as computed usersets, so the
// store answers a `read` check for an owner without the caller enumerating —
// this function exists for callers reasoning ABOUT the vocabulary (migration,
// validation, UI), not as a substitute for asking the store.
func Implies(p, q Permission) bool {
	pr, pok := strength(p)
	qr, qok := strength(q)
	if !pok || !qok {
		return false
	}
	return pr >= qr
}

// strength orders the vocabulary. Unexported: the numbers are an implementation
// detail of Implies and must not become a wire value, or a reordering would
// silently change stored data.
func strength(p Permission) (int, bool) {
	switch p {
	case PermRead:
		return 1, true
	case PermQuery:
		return 2, true
	case PermWrite:
		return 3, true
	case PermShare:
		return 4, true
	case PermOwn:
		return 5, true
	default:
		return 0, false
	}
}

// Relation maps a permission and subject kind onto the FGA relation to write.
//
// THIS IS THE TOTAL FUNCTION target design §5.20 #2 requires. It is the only
// sanctioned way to produce a relation; building one by string concatenation at
// a call site is the defect this package replaces.
//
// Agents receive `delegated_<permission>`. Every delegated relation has a
// user-side twin, which is what makes the dual check possible — the pinned
// model previously defined delegated_querier with no `querier` to bound it
// against, so "an agent can never exceed its user" had nothing to check for
// query operations.
func Relation(p Permission, k SubjectKind) (string, error) {
	if _, ok := strength(p); !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownPermission, p)
	}

	switch k {
	case SubjectUser:
		return string(p), nil

	case SubjectOrganization:
		// An organization may own a resource outright.
		return string(p), nil

	case SubjectGroup:
		// A group cannot OWN. Ownership carries deletion and the right to
		// create other owners, and a group's membership is managed elsewhere
		// and asynchronously — so ownership vested in a group could be
		// acquired by adding a member, with no owner ever approving it.
		if p == PermOwn {
			return "", fmt.Errorf("%w: %q cannot be granted to a group", ErrPermissionNotGrantable, p)
		}
		return string(p), nil

	case SubjectAgent:
		// Agents never hold a user's own relation.
		//
		// An agent cannot hold `own` for the same reason a group cannot, and
		// more sharply: an agent acts under a delegation that can be revoked,
		// and a revocable owner is not an owner. It cannot hold `share`
		// either, or a delegation could widen itself by granting to another
		// agent — the exact escalation the dual check exists to prevent.
		if p == PermOwn || p == PermShare {
			return "", fmt.Errorf("%w: %q cannot be delegated to an agent", ErrPermissionNotGrantable, p)
		}
		return "delegated_" + string(p), nil

	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownSubjectKind, k)
	}
}

// ParsePermission converts stored or wire input into a Permission.
//
// Deliberately STRICT — no case folding, no trimming, no aliases. The value is
// read back from a durable grant and from a queue, and a lenient parser is how
// "Read", "read " and "READ" become three permissions that mostly behave the
// same until one of them does not.
func ParsePermission(s string) (Permission, error) {
	p := Permission(s)
	if _, ok := strength(p); !ok {
		return "", fmt.Errorf("%w: %q", ErrUnknownPermission, s)
	}
	return p, nil
}

// ParseSubjectKind converts stored or wire input into a SubjectKind. Strict for
// the same reason as ParsePermission.
func ParseSubjectKind(s string) (SubjectKind, error) {
	k := SubjectKind(s)
	switch k {
	case SubjectUser, SubjectOrganization, SubjectGroup, SubjectAgent:
		return k, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrUnknownSubjectKind, s)
	}
}

// ValidRelation reports whether a relation string is one this vocabulary can
// produce.
//
// dx-authz-go uses it to RE-VALIDATE tuples arriving over the queue before
// writing them. The projection is asynchronous, so a producer running older
// code — one that still writes `api` or `databank` — must be rejected at the
// consumer rather than trusted because it is internal.
func ValidRelation(relation string) bool {
	for _, p := range Permissions() {
		for _, k := range SubjectKinds() {
			if r, err := Relation(p, k); err == nil && r == relation {
				return true
			}
		}
	}
	return false
}
