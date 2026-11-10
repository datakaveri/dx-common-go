package constraint

import (
	"net/netip"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, _ := time.Parse(time.RFC3339, s)
	return t
}

func TestEmptyConditionSetAllows(t *testing.T) {
	if o := New().Evaluate(ConditionSet{}, Input{Now: time.Now()}); !o.Allowed {
		t.Fatalf("empty conditions must allow, got %+v", o)
	}
}

func TestValidityWindow(t *testing.T) {
	nb := at("2026-01-01T00:00:00Z")
	na := at("2026-12-31T23:59:59Z")
	cs := ConditionSet{NotBefore: &nb, NotAfter: &na}
	e := New()
	if o := e.Evaluate(cs, Input{Now: at("2025-06-01T00:00:00Z")}); o.Allowed || o.Reason != "not_yet_valid" {
		t.Fatalf("before window: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Now: at("2027-01-01T00:00:00Z")}); o.Allowed || o.Reason != "expired" {
		t.Fatalf("after window: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Now: at("2026-06-01T00:00:00Z")}); !o.Allowed {
		t.Fatalf("inside window must allow: %+v", o)
	}
}

func TestRoleIntersection(t *testing.T) {
	cs := ConditionSet{AllowedRoles: []string{"consumer", "provider"}}
	e := New()
	if o := e.Evaluate(cs, Input{Roles: []string{"consumer"}}); !o.Allowed {
		t.Fatalf("matching role must allow: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Roles: []string{"admin"}}); o.Allowed || o.Reason != "role_not_permitted" {
		t.Fatalf("non-matching role must deny: %+v", o)
	}
	if o := e.Evaluate(cs, Input{}); o.Allowed {
		t.Fatal("no roles when roles required must deny")
	}
}

func TestPurposeMembership(t *testing.T) {
	cs := ConditionSet{AllowedPurposes: []string{"research"}}
	e := New()
	if o := e.Evaluate(cs, Input{Purpose: "research"}); !o.Allowed {
		t.Fatalf("allowed purpose: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Purpose: "marketing"}); o.Allowed || o.Reason != "purpose_not_permitted" {
		t.Fatalf("disallowed purpose: %+v", o)
	}
	if o := e.Evaluate(cs, Input{}); o.Allowed || o.Reason != "purpose_required" {
		t.Fatalf("missing required purpose must deny: %+v", o)
	}
}

func TestSourceCIDR(t *testing.T) {
	cs := ConditionSet{SourceCIDRs: []string{"10.0.0.0/8", "192.168.1.0/24"}}
	e := New()
	if o := e.Evaluate(cs, Input{SourceIP: netip.MustParseAddr("10.20.30.40")}); !o.Allowed {
		t.Fatalf("in-range ip must allow: %+v", o)
	}
	if o := e.Evaluate(cs, Input{SourceIP: netip.MustParseAddr("8.8.8.8")}); o.Allowed || o.Reason != "source_not_permitted" {
		t.Fatalf("out-of-range ip must deny: %+v", o)
	}
	if o := e.Evaluate(cs, Input{}); o.Allowed || o.Reason != "source_not_permitted" {
		t.Fatalf("missing source ip when required must deny: %+v", o)
	}
	if o := e.Evaluate(ConditionSet{SourceCIDRs: []string{"not-a-cidr"}}, Input{SourceIP: netip.MustParseAddr("10.0.0.1")}); o.Allowed || o.Reason != "source_cidr_invalid" {
		t.Fatalf("malformed cidr must fail closed: %+v", o)
	}
}

func TestAssurance(t *testing.T) {
	cs := ConditionSet{MinAssurance: "mfa"}
	e := New()
	if o := e.Evaluate(cs, Input{Assurance: "mfa"}); !o.Allowed {
		t.Fatalf("meeting assurance must allow: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Assurance: "hardware"}); !o.Allowed {
		t.Fatalf("exceeding assurance must allow: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Assurance: "password"}); o.Allowed || o.Reason != "assurance_insufficient" {
		t.Fatalf("below assurance must deny: %+v", o)
	}
	if o := e.Evaluate(cs, Input{Assurance: "bogus"}); o.Allowed {
		t.Fatal("unknown input assurance must not satisfy a requirement (fail closed)")
	}
	if o := e.Evaluate(ConditionSet{MinAssurance: "quantum"}, Input{Assurance: "mfa"}); o.Allowed || o.Reason != "assurance_requirement_invalid" {
		t.Fatalf("unknown required assurance must fail closed: %+v", o)
	}
}

func TestAllConditionsAnded(t *testing.T) {
	nb := at("2026-01-01T00:00:00Z")
	cs := ConditionSet{
		NotBefore:       &nb,
		AllowedRoles:    []string{"consumer"},
		AllowedPurposes: []string{"research"},
		MinAssurance:    "mfa",
	}
	good := Input{Now: at("2026-06-01T00:00:00Z"), Roles: []string{"consumer"}, Purpose: "research", Assurance: "mfa"}
	if o := New().Evaluate(cs, good); !o.Allowed {
		t.Fatalf("all conditions met must allow: %+v", o)
	}
	// Break one at a time; each must deny.
	bad := good
	bad.Assurance = "password"
	if o := New().Evaluate(cs, bad); o.Allowed {
		t.Fatal("one failing condition must deny the whole set")
	}
}
