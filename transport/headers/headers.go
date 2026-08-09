// Package headers carries the internal subject headers naming the END USER an
// internal call speaks for.
//
// # Why these headers are no longer signed
//
// They used to carry an HMAC over id|email|roles|org|issued_at, and every
// service held the same shared secret. That signature did two jobs, and only
// one of them was real:
//
//   - It authenticated the CALLER — implicitly, as "somebody holding the
//     secret". That is what review finding C-02 identified as broken, and it is
//     replaced by platform/security/workload's audience-bound credential, which
//     names a specific workload cryptographically.
//   - It protected SUBJECT INTEGRITY — which upstream may say which user a
//     request speaks for. That job is now done by the asserter list: a verified
//     workload may assert a subject only if the receiving service names it in
//     workload_verifier.subject_asserters.
//
// Removing the signature is not a weakening, and the reason is specific rather
// than reassuring:
//
//   - Every asserter held the SAME secret, so the signature proved membership of
//     a group, never identity. It could not distinguish the gateway from any
//     other service, which is exactly the authority C-02 is about. An explicit
//     asserter list checked against a cryptographic caller identity is strictly
//     stronger than a signature every caller can produce.
//   - The workload credential is itself a bearer token sent in plaintext
//     (ADR-06 §3.1 defers mTLS; the mesh terminates TLS). Anyone positioned to
//     rewrite X-Subject-Id in transit can already read and replay that token, so
//     the HMAC protected nothing the threat model does not already concede.
//
// The consequence a reader must not miss: THESE HEADERS ARE ONLY TRUSTWORTHY
// BEHIND A VERIFIED WORKLOAD. Parse does no authentication — it cannot, there is
// nothing to check. auth/resolver enforces that by refusing to read them unless
// a verified workload principal is on the request context, so a service with no
// verifier cannot accidentally trust a client-supplied X-Subject-Id.
//
// Headers minted:
//
//	X-Subject-Id          stable user identifier (sub claim)
//	X-Subject-Email       optional
//	X-Subject-Roles       comma-joined realm roles
//	X-Subject-Org-Id      organisation the user belongs to
//	X-Agent-Subject       acting agent (RFC 8693 act.sub) — delegated calls only
//	X-Delegation-Id       delegation grant reference — delegated calls only
//
// ROADMAP P0-17 stage 2; ADR-06.
package headers

import (
	"errors"
	"net/http"
	"sort"
	"strings"

	"github.com/datakaveri/dx-common-go/auth"
)

// Header names — exported so callers can also strip/inspect them.
const (
	HdrSubjectID    = "X-Subject-Id"
	HdrSubjectEmail = "X-Subject-Email"
	HdrSubjectRoles = "X-Subject-Roles"
	HdrSubjectOrgID = "X-Subject-Org-Id"
	// Agent headers are minted only for delegated (agent-acting) requests.
	HdrAgentSubject = "X-Agent-Subject"
	HdrDelegationID = "X-Delegation-Id"
)

// All is every header this package mints.
//
// It exists so an edge that must STRIP client-supplied subject headers cannot
// miss one: the gateway does exactly that, and a header it forgot to strip is a
// header a client can set. Adding a header above without adding it here is the
// mistake this list prevents.
var All = []string{
	HdrSubjectID, HdrSubjectEmail, HdrSubjectRoles,
	HdrSubjectOrgID, HdrAgentSubject, HdrDelegationID,
}

// ErrNoSubject indicates the request asserts no subject at all.
//
// It is NOT an authentication failure: a service calling on its own behalf
// legitimately asserts no user, and the handler decides whether it needs one.
var ErrNoSubject = errors.New("request asserts no subject")

// ErrInvalidSubject indicates subject headers that are present but malformed.
var ErrInvalidSubject = errors.New("invalid subject headers")

// Project returns the X-Subject-* headers naming user. The caller copies them
// onto the outgoing request with Apply.
func Project(user auth.DxUser) (http.Header, error) {
	// A blank subject id would authenticate an anonymous principal downstream
	// (and could match records with an empty owner). Refuse to mint it.
	if strings.TrimSpace(user.ID) == "" {
		return nil, errors.New("headers.Project: user ID is required")
	}
	// Roles are comma-joined into one header, so a role containing a comma
	// would split into two on the far side. Reject rather than silently
	// fabricate a role the user does not hold.
	for _, r := range user.Roles {
		if strings.Contains(r, ",") {
			return nil, errors.New("headers.Project: a role must not contain ','")
		}
	}

	h := http.Header{}
	h.Set(HdrSubjectID, user.ID)
	if user.Email != "" {
		h.Set(HdrSubjectEmail, user.Email)
	}
	if joined := joinRoles(user.Roles); joined != "" {
		h.Set(HdrSubjectRoles, joined)
	}
	if user.OrganisationID != "" {
		h.Set(HdrSubjectOrgID, user.OrganisationID)
	}
	if user.AgentSubject != "" {
		h.Set(HdrAgentSubject, user.AgentSubject)
	}
	if user.DelegationID != "" {
		h.Set(HdrDelegationID, user.DelegationID)
	}
	return h, nil
}

// Apply copies projected headers onto an outbound request, overwriting any
// existing values for those header names.
func Apply(req *http.Request, projected http.Header) {
	for k := range projected {
		req.Header.Set(k, projected.Get(k))
	}
}

// Strip removes every subject header from h.
//
// An edge that accepts requests from outside MUST call this before projecting
// its own, or a client can name whichever user it likes. The gateway does; this
// is the function that makes "did we strip them all" answerable in one place.
func Strip(h http.Header) {
	for _, name := range All {
		h.Del(name)
	}
}

// Parse reads the subject headers.
//
// It performs NO authentication and cannot: there is nothing to verify. The
// authority to assert these headers is established BEFORE this is reached, by
// platform/security/workload's verifier and its asserter list. Calling Parse on
// a request whose caller has not been verified trusts whatever the client sent.
func Parse(h http.Header) (auth.DxUser, error) {
	id := h.Get(HdrSubjectID)
	if strings.TrimSpace(id) == "" {
		return auth.DxUser{}, ErrNoSubject
	}
	return auth.DxUser{
		ID:             id,
		Email:          h.Get(HdrSubjectEmail),
		Roles:          splitRoles(h.Get(HdrSubjectRoles)),
		OrganisationID: h.Get(HdrSubjectOrgID),
		AgentSubject:   h.Get(HdrAgentSubject),
		DelegationID:   h.Get(HdrDelegationID),
	}, nil
}

// Asserts reports whether h claims to speak for a user.
//
// Used by the workload gate to decide whether the asserter check applies, so it
// must agree with Parse about what "asserts a subject" means — hence one
// definition here rather than a repeated h.Get(...) != "" in three packages.
func Asserts(h http.Header) bool {
	return strings.TrimSpace(h.Get(HdrSubjectID)) != ""
}

// --- internals --------------------------------------------------------------

func joinRoles(roles []string) string {
	if len(roles) == 0 {
		return ""
	}
	// Sorted so the projection is deterministic — it makes a header diff in a
	// trace or a test comparison stable, which is the only property still
	// wanted now that nothing signs over it.
	sorted := append([]string(nil), roles...)
	sort.Strings(sorted)
	return strings.Join(sorted, ",")
}

func splitRoles(joined string) []string {
	if joined == "" {
		return nil
	}
	parts := strings.Split(joined, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
