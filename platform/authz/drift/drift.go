package drift

import (
	"sort"
	"strings"

	"github.com/datakaveri/dx-common-go/platform/authz/manifest"
	httpx "github.com/datakaveri/dx-common-go/platform/http"
)

// Package drift checks that a service's ROUTER and its OpenAPI manifest agree.
//
// # Why this is its own package
//
// It imports the manifest compiler, which imports kin-openapi. Living in
// platform/http would add that dependency to EVERY service using httpx —
// `fleet-check --tidy` immediately moved go.mod in four services with no use
// for it.
//
// Same trap the P0-2 session hit when minting shared a package with
// verification and pulled gRPC into two services that had none. The rule that
// produced applies here: a helper only some services need does not belong in
// the package all of them import.

// Drift detection between a service's ROUTER and its OpenAPI manifest.
//
// # Why this exists (AUTHZ-1 pilot)
//
// Route.OpID has always been documented as "used by AssertNoDrift". There was
// no AssertNoDrift. The field was carried, populated by every service, and
// checked by nothing — so `dx-catalogue-go`'s router called an operation
// `search` while its spec called the same operation `searchGet`, and nothing
// noticed.
//
// That matters more now than it did. The manifest makes the SPEC authoritative
// for policy: which operations exist, whether each needs authentication, and
// which permission it requires. If the router and the spec disagree about which
// operation a request is, then the policy compiled from the spec is being
// applied to an endpoint the router thinks is something else.
//
// The two failure directions are not symmetric, and both are checked:
//
//	route with no spec operation  — an endpoint the manifest does not cover, so
//	                                the gateway denies it (fail closed, but the
//	                                service believes it is serving)
//	spec operation with no route  — policy for an endpoint that does not exist;
//	                                harmless today, and it is how a stale entry
//	                                survives long enough to be re-enabled later
//
// # Public must mean the same thing on both sides
//
// A route marked Public bypasses authentication in the router. An operation
// declared `authentication: none` bypasses it at the gateway. If those two sets
// differ, one layer is protecting something the other is not — which is the
// same class as P0-9's "internal was enforced only by a missing route".

// TB is the subset of testing.TB this file needs.
//
// Declared here rather than importing testing into a non-test file: a service
// calls AssertNoDrift from its own test, and pulling the testing package into
// the production build graph to do it would be worse than an interface.
type TB interface {
	Helper()
	Errorf(format string, args ...any)
	Fatalf(format string, args ...any)
}

// PlatformMounted names operations the PLATFORM router serves, not the
// service's RouteSet — health probes today.
//
// They belong in the spec (the gateway needs their policy, and a health probe
// declaring `authentication: none` is a real statement) but they will never
// appear in a service's routes, so treating their absence as drift would mean
// every service permanently failing its own drift check.
//
// Named explicitly rather than pattern-matched on "/healthz": a check that
// exempts by path prefix would also exempt a service's own endpoint that
// happened to start with the same string.
var PlatformMounted = []string{"getLive", "getReady"}

// AssertNoDrift fails t if the router and the compiled manifest disagree.
//
// It reports EVERY disagreement rather than the first, because a service
// aligning a spec wants one list.
//
// exempt names spec operations the service's RouteSet does not serve; pass
// PlatformMounted for the usual case.
func AssertNoDrift(t TB, m *manifest.Manifest, sets []httpx.RouteSet, exempt ...string) {
	t.Helper()
	if m == nil {
		t.Fatalf("AssertNoDrift: nil manifest")
		return
	}

	// Spec side, keyed by operationId.
	spec := make(map[string]manifest.Operation, len(m.Operations))
	for _, op := range m.Operations {
		spec[op.OperationID] = op
	}

	// Router side. Health and docs routes are part of the router but are not
	// business operations; a service that declares them in its spec is welcome
	// to, and one that does not is not drifting.
	routes := map[string]httpx.Route{}
	for _, set := range sets {
		for _, r := range set.Routes {
			if r.OpID == "" {
				t.Errorf("route %s %s has no OpID — it cannot be matched against the spec, so "+
					"its policy cannot be checked", r.Method, set.Prefix+r.Path)
				continue
			}
			if prev, dup := routes[r.OpID]; dup {
				t.Errorf("OpID %q is used by both %s %s and %s %s — an operation id must "+
					"identify one operation, since it is how a decision is attributed",
					r.OpID, prev.Method, prev.Path, r.Method, r.Path)
				continue
			}
			routes[r.OpID] = r
		}
	}

	for _, id := range sortedKeys(routes) {
		r := routes[id]
		op, ok := spec[id]
		if !ok {
			t.Errorf("router serves %s %s as %q, which the spec does not declare — the manifest "+
				"does not cover it, so the gateway denies a request the service believes it serves",
				r.Method, r.Path, id)
			continue
		}
		if !strings.EqualFold(op.Method, r.Method) {
			t.Errorf("%q is %s in the router and %s in the spec", id, r.Method, op.Method)
		}
		// Public on one side and not the other means one layer is protecting
		// what the other is not.
		specPublic := op.Authentication == manifest.AuthNone
		if specPublic != r.Public {
			t.Errorf("%q: router Public=%v but spec authentication=%q — one layer authenticates "+
				"this operation and the other does not", id, r.Public, op.Authentication)
		}
	}

	exemptSet := map[string]bool{}
	for _, id := range exempt {
		exemptSet[id] = true
	}
	for _, id := range sortedKeys(spec) {
		if exemptSet[id] {
			continue
		}
		if _, ok := routes[id]; !ok {
			op := spec[id]
			t.Errorf("spec declares %q (%s %s) which the router does not serve — policy for an "+
				"endpoint that does not exist, and how a stale entry survives to be re-enabled later",
				id, op.Method, op.Path)
		}
	}
}

func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
