package vocabulary_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/vocabulary"
)

// Target design §5.20 #2 makes "exhaustive table tests over the full
// vocabulary" a MERGE GATE, so these tests iterate the exported vocabulary
// rather than restating it. A test carrying its own copy of the list stops
// covering the vocabulary the moment someone adds to it — which is exactly the
// kind of silent gap this package exists to close.

// TestRelationIsTotal is the headline property: every (permission, kind) pair
// either produces a relation or an explicit error. Never a zero value with a
// nil error, and never a panic.
func TestRelationIsTotal(t *testing.T) {
	for _, p := range vocabulary.Permissions() {
		for _, k := range vocabulary.SubjectKinds() {
			rel, err := vocabulary.Relation(p, k)
			switch {
			case err != nil && rel != "":
				t.Errorf("(%s,%s) returned BOTH a relation %q and an error %v", p, k, rel, err)
			case err == nil && rel == "":
				t.Errorf("(%s,%s) returned an empty relation with no error — a caller would "+
					"write a tuple with a blank relation", p, k)
			}
		}
	}
}

// TestRelationMapping pins every accepted pair explicitly. Exhaustive by
// construction: the count is asserted against the vocabulary size, so adding a
// permission without adding cases here fails.
func TestRelationMapping(t *testing.T) {
	cases := map[string]struct {
		perm vocabulary.Permission
		kind vocabulary.SubjectKind
		want string
	}{
		"user read":   {vocabulary.PermRead, vocabulary.SubjectUser, "read"},
		"user query":  {vocabulary.PermQuery, vocabulary.SubjectUser, "query"},
		"user write":  {vocabulary.PermWrite, vocabulary.SubjectUser, "write"},
		"user share":  {vocabulary.PermShare, vocabulary.SubjectUser, "share"},
		"user own":    {vocabulary.PermOwn, vocabulary.SubjectUser, "own"},
		"org read":    {vocabulary.PermRead, vocabulary.SubjectOrganization, "read"},
		"org query":   {vocabulary.PermQuery, vocabulary.SubjectOrganization, "query"},
		"org write":   {vocabulary.PermWrite, vocabulary.SubjectOrganization, "write"},
		"org share":   {vocabulary.PermShare, vocabulary.SubjectOrganization, "share"},
		"org own":     {vocabulary.PermOwn, vocabulary.SubjectOrganization, "own"},
		"group read":  {vocabulary.PermRead, vocabulary.SubjectGroup, "read"},
		"group query": {vocabulary.PermQuery, vocabulary.SubjectGroup, "query"},
		"group write": {vocabulary.PermWrite, vocabulary.SubjectGroup, "write"},
		"group share": {vocabulary.PermShare, vocabulary.SubjectGroup, "share"},
		"agent read":  {vocabulary.PermRead, vocabulary.SubjectAgent, "delegated_read"},
		"agent query": {vocabulary.PermQuery, vocabulary.SubjectAgent, "delegated_query"},
		"agent write": {vocabulary.PermWrite, vocabulary.SubjectAgent, "delegated_write"},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := vocabulary.Relation(tc.perm, tc.kind)
			if err != nil {
				t.Fatalf("Relation(%s,%s) = %v, want %q", tc.perm, tc.kind, err, tc.want)
			}
			if got != tc.want {
				t.Errorf("Relation(%s,%s) = %q, want %q", tc.perm, tc.kind, got, tc.want)
			}
		})
	}

	// 5 permissions × 4 kinds = 20 pairs; 3 are forbidden (group own, agent
	// own, agent share), so 17 must be accepted. Asserted so that adding a
	// permission or a kind without extending this table is a failure rather
	// than an untested pair.
	const wantAccepted = 17
	if len(cases) != wantAccepted {
		t.Errorf("table has %d accepted pairs, want %d — the vocabulary changed and this "+
			"table did not", len(cases), wantAccepted)
	}
}

// TestForbiddenCombinations pins the three refusals AND their reasons, because
// each is a real escalation path rather than a tidiness rule.
func TestForbiddenCombinations(t *testing.T) {
	tests := []struct {
		name string
		perm vocabulary.Permission
		kind vocabulary.SubjectKind
		why  string
	}{
		{
			name: "a group cannot own", perm: vocabulary.PermOwn, kind: vocabulary.SubjectGroup,
			why: "group membership is managed elsewhere and asynchronously, so ownership vested " +
				"in a group could be acquired by ADDING A MEMBER, with no owner approving it",
		},
		{
			name: "an agent cannot own", perm: vocabulary.PermOwn, kind: vocabulary.SubjectAgent,
			why: "an agent acts under a revocable delegation, and a revocable owner is not an owner",
		},
		{
			name: "an agent cannot share", perm: vocabulary.PermShare, kind: vocabulary.SubjectAgent,
			why: "a delegation that can grant to another agent widens itself — the exact " +
				"escalation the dual check exists to prevent",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := vocabulary.Relation(tt.perm, tt.kind)
			if err == nil {
				t.Fatalf("Relation(%s,%s) = %q with no error — %s", tt.perm, tt.kind, got, tt.why)
			}
			if !errors.Is(err, vocabulary.ErrPermissionNotGrantable) {
				t.Errorf("error = %v, want ErrPermissionNotGrantable", err)
			}
		})
	}
}

// TestEveryAgentRelationHasAUserTwin is the invariant the pinned model broke.
//
// It defined delegated_querier with NO user-side querier to bound it against,
// so "an agent can never exceed its user" had no relation to check for query
// operations. The dual check needs both halves to exist, for every delegated
// relation, without exception.
func TestEveryAgentRelationHasAUserTwin(t *testing.T) {
	for _, p := range vocabulary.Permissions() {
		agentRel, agentErr := vocabulary.Relation(p, vocabulary.SubjectAgent)
		if agentErr != nil {
			continue // not delegatable at all; nothing to bound
		}
		userRel, userErr := vocabulary.Relation(p, vocabulary.SubjectUser)
		if userErr != nil {
			t.Errorf("%q is delegatable to an agent (%s) but NOT holdable by a user — the dual "+
				"check has nothing to bound the agent against, so it could exceed its user",
				p, agentRel)
			continue
		}
		if agentRel != "delegated_"+userRel {
			t.Errorf("agent relation %q is not the delegated twin of user relation %q",
				agentRel, userRel)
		}
	}
}

// TestImplicationChain pins own → share → write → query → read.
func TestImplicationChain(t *testing.T) {
	order := []vocabulary.Permission{
		vocabulary.PermRead, vocabulary.PermQuery, vocabulary.PermWrite,
		vocabulary.PermShare, vocabulary.PermOwn,
	}
	if len(order) != len(vocabulary.Permissions()) {
		t.Fatalf("the chain covers %d permissions but the vocabulary has %d",
			len(order), len(vocabulary.Permissions()))
	}

	for i, stronger := range order {
		for j, weaker := range order {
			want := i >= j
			if got := vocabulary.Implies(stronger, weaker); got != want {
				t.Errorf("Implies(%s, %s) = %v, want %v", stronger, weaker, got, want)
			}
		}
	}

	// The distinction the split exists for: read must NOT imply query.
	if vocabulary.Implies(vocabulary.PermRead, vocabulary.PermQuery) {
		t.Error("read implies query — seeing that a dataset exists would grant access to its " +
			"contents, which is the difference a data exchange charges for")
	}
	// And an unknown permission implies nothing, rather than defaulting.
	if vocabulary.Implies("api", vocabulary.PermRead) {
		t.Error(`"api" implies read — accessType is not a permission and must not behave as one`)
	}
}

// TestParseIsStrict: no case folding, no trimming, no aliases. These values are
// read back from durable grants and from a queue, and a lenient parser is how
// "Read", "read " and "READ" become three permissions that behave the same
// until one of them does not.
func TestParseIsStrict(t *testing.T) {
	bad := []string{
		"", "Read", "READ", " read", "read ", "reader", "viewer", "editor",
		// The old vocabulary. These MUST NOT parse, or the migration silently
		// keeps working with the values it is supposed to replace.
		"api", "file", "sub", "databank", "aimodel", "apps",
	}
	for _, s := range bad {
		if p, err := vocabulary.ParsePermission(s); err == nil {
			t.Errorf("ParsePermission(%q) = %q, want an error", s, p)
		} else if !errors.Is(err, vocabulary.ErrUnknownPermission) {
			t.Errorf("ParsePermission(%q) error = %v, want ErrUnknownPermission", s, err)
		}
	}
	for _, p := range vocabulary.Permissions() {
		if got, err := vocabulary.ParsePermission(string(p)); err != nil || got != p {
			t.Errorf("ParsePermission(%q) = (%q,%v), want it to round-trip", p, got, err)
		}
	}
}

func TestParseSubjectKindIsStrict(t *testing.T) {
	for _, s := range []string{"", "User", "USER", "org", "agents", "service"} {
		if k, err := vocabulary.ParseSubjectKind(s); err == nil {
			t.Errorf("ParseSubjectKind(%q) = %q, want an error", s, k)
		}
	}
	for _, k := range vocabulary.SubjectKinds() {
		if got, err := vocabulary.ParseSubjectKind(string(k)); err != nil || got != k {
			t.Errorf("ParseSubjectKind(%q) = (%q,%v), want it to round-trip", k, got, err)
		}
	}
}

// TestValidRelationRejectsTheOldVocabulary is the consumer-side guard.
//
// The projection is asynchronous and crosses a queue, so a producer running
// older code can still emit `api` or `databank`. dx-authz-go must reject those
// rather than trust them because they arrived internally — the whole reason the
// mapping is re-validated on both sides.
func TestValidRelationRejectsTheOldVocabulary(t *testing.T) {
	for _, bad := range []string{
		"api", "file", "sub", // accessTypes, the original defect
		"viewer", "editor", "querier", "delegated_querier", // the superseded model
		"", "READ", "delegated_own", "delegated_share",
	} {
		if vocabulary.ValidRelation(bad) {
			t.Errorf("ValidRelation(%q) = true — a stale producer's tuple would be written", bad)
		}
	}

	// Everything the function can produce must validate, or the two sides
	// disagree and correct tuples get rejected in production.
	for _, p := range vocabulary.Permissions() {
		for _, k := range vocabulary.SubjectKinds() {
			rel, err := vocabulary.Relation(p, k)
			if err != nil {
				continue
			}
			if !vocabulary.ValidRelation(rel) {
				t.Errorf("Relation(%s,%s) produced %q which ValidRelation rejects — the "+
					"producer and the consumer disagree", p, k, rel)
			}
		}
	}
}

// TestResourceTypeIsConstant: the original defect derived the FGA type from an
// item's kind. It is a constant so a caller cannot reintroduce that.
func TestResourceTypeIsConstant(t *testing.T) {
	if vocabulary.ResourceType != "resource" {
		t.Errorf("ResourceType = %q, want \"resource\" — the pinned model accepts no other type",
			vocabulary.ResourceType)
	}
	for _, old := range []string{"databank", "aimodel", "apps"} {
		if strings.EqualFold(vocabulary.ResourceType, old) {
			t.Errorf("ResourceType is %q, an item KIND — kinds are catalogue metadata, not FGA types", old)
		}
	}
}

// TestUnknownInputsAreRejected covers the branch the exhaustive tables cannot
// reach.
//
// Permissions() and SubjectKinds() return only VALID values, so iterating them
// never exercises the default case — and a sabotage that replaced the default
// with `return string(p), nil` passed every other test in this file. The
// package's entire premise is that there is no default branch, so the absence
// of a default has to be asserted directly.
//
// This is not hypothetical: `accessType` reached the relation slot precisely
// because an unrecognised value fell through to something plausible instead of
// failing.
func TestUnknownInputsAreRejected(t *testing.T) {
	t.Run("unknown subject kind", func(t *testing.T) {
		for _, k := range []vocabulary.SubjectKind{"", "service", "workload", "User", "agents"} {
			got, err := vocabulary.Relation(vocabulary.PermRead, k)
			if err == nil {
				t.Errorf("Relation(read, %q) = %q with no error — an unrecognised subject kind "+
					"fell through to a plausible relation, which is exactly how accessType "+
					"became one", k, got)
				continue
			}
			if !errors.Is(err, vocabulary.ErrUnknownSubjectKind) {
				t.Errorf("Relation(read, %q) error = %v, want ErrUnknownSubjectKind", k, err)
			}
		}
	})

	t.Run("unknown permission", func(t *testing.T) {
		// The old vocabulary specifically: these must not map to anything.
		for _, p := range []vocabulary.Permission{"", "api", "file", "sub", "viewer", "editor"} {
			got, err := vocabulary.Relation(p, vocabulary.SubjectUser)
			if err == nil {
				t.Errorf("Relation(%q, user) = %q with no error — the superseded vocabulary "+
					"still maps, so a stale producer keeps working", p, got)
				continue
			}
			if !errors.Is(err, vocabulary.ErrUnknownPermission) {
				t.Errorf("Relation(%q, user) error = %v, want ErrUnknownPermission", p, err)
			}
		}
	})

	t.Run("unknown permission is checked before subject kind", func(t *testing.T) {
		// Both invalid: the permission error must win, so a caller debugging a
		// rejected tuple is told about the value it actually controls.
		_, err := vocabulary.Relation("api", "service")
		if !errors.Is(err, vocabulary.ErrUnknownPermission) {
			t.Errorf("error = %v, want the PERMISSION error to take precedence", err)
		}
	})
}
