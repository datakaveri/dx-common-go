package beckn

import (
	"testing"

	"github.com/datakaveri/dx-common-go/platform/authz/decision"
)

func TestTermsHashStableAndOrderIndependent(t *testing.T) {
	a := MaterialTerms{OfferID: "o1", Provider: "p", Amount: "100", Currency: "INR", Payee: "acc", Extra: map[string]string{"fee": "10", "note": "x"}}
	b := MaterialTerms{OfferID: "o1", Provider: "p", Amount: "100", Currency: "INR", Payee: "acc", Extra: map[string]string{"note": "x", "fee": "10"}}
	if a.Hash() != b.Hash() {
		t.Fatal("map ordering must not affect the terms hash")
	}
	c := a
	c.Amount = "999"
	if a.Hash() == c.Hash() {
		t.Fatal("a changed amount must change the terms hash")
	}
	if len(a.Hash()) < 20 || a.Hash()[:7] != "sha256:" {
		t.Fatalf("hash format: %s", a.Hash())
	}
}

func TestBuySideDiscoverUsesVenue(t *testing.T) {
	req, err := BuySide(BuyParams{Op: OpDiscover, Subject: "user-1", Agent: "agent-9", OperationID: "beckn.discover"})
	if err != nil {
		t.Fatal(err)
	}
	if req.Resource.Type != VenueResourceType || req.Resource.ID != VenueNFH {
		t.Fatalf("discover must target the venue: %+v", req.Resource)
	}
	if req.Action.Name != "query" {
		t.Fatalf("discover action: %q", req.Action.Name)
	}
	if req.Subject.ID != "user-1" || req.Context.DX.Actor.ID != "agent-9" {
		t.Fatalf("subject/actor wrong: %+v", req)
	}
}

func TestBuySideConfirmRequiresTermsAndBinds(t *testing.T) {
	// Missing terms -> fail closed.
	if _, err := BuySide(BuyParams{Op: OpConfirm, Subject: "user-1", ResourceID: "order-1"}); err == nil {
		t.Fatal("confirm without material terms must fail closed")
	}
	terms := &MaterialTerms{OfferID: "o1", Amount: "4500", Currency: "INR", Payee: "prov-1"}
	req, err := BuySide(BuyParams{Op: OpConfirm, Subject: "user-1", Agent: "a", ResourceID: "order-1", OperationID: "beckn.confirm", Terms: terms})
	if err != nil {
		t.Fatal(err)
	}
	if req.Action.Name != "write" {
		t.Fatalf("confirm action: %q", req.Action.Name)
	}
	if req.Context.DX.Request == nil || req.Context.DX.Request.TermsHash != terms.Hash() {
		t.Fatalf("terms hash not bound: %+v", req.Context.DX.Request)
	}
	if req.Context.DX.Request.Amount != "4500" || req.Context.DX.Request.Payee != "prov-1" {
		t.Fatalf("material terms not carried: %+v", req.Context.DX.Request)
	}
}

func TestBuySideNonDiscoverRequiresResource(t *testing.T) {
	if _, err := BuySide(BuyParams{Op: OpStatus, Subject: "user-1"}); err == nil {
		t.Fatal("status without a resource id must fail")
	}
}

func TestRiskTiers(t *testing.T) {
	if !IsHighRisk(OpConfirm) || !IsHighRisk(OpInit) || !IsHighRisk(OpCancel) {
		t.Fatal("init/confirm/cancel must be high risk")
	}
	if IsHighRisk(OpDiscover) || IsHighRisk(OpStatus) {
		t.Fatal("discover/status must not be high risk")
	}
	if RiskOf(OpSelect) != RiskMedium || RiskOf(OpRate) != RiskMedium {
		t.Fatal("select/rate must be medium")
	}
	if RiskOf("beckn.unknown") != "" {
		t.Fatal("unknown op has no risk")
	}
}

func TestInboundCorrelatedCallback(t *testing.T) {
	req, c, err := InboundRequest(InboundMsg{
		Method: "on_confirm", ParticipantID: "bpp-1", TransactionID: "txn-1", MessageID: "m-1",
		KnownTransaction: true, OperationID: "beckn.on_confirm",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c != CaseCorrelated {
		t.Fatalf("on_confirm must be a correlated callback, got %s", c)
	}
	if req.Subject.Type != decision.SubjectNetworkParticipant || req.Subject.ID != "bpp-1" {
		t.Fatalf("subject wrong: %+v", req.Subject)
	}
	// Authority is the transaction we own.
	if req.Resource.ID != "txn-1" {
		t.Fatalf("correlated callback must target the transaction: %+v", req.Resource)
	}
	if req.Action.Name != "write" {
		t.Fatalf("on_confirm action: %q", req.Action.Name)
	}
	if req.Context.DX.Actor.TransactionID != "txn-1" || req.Context.DX.Actor.MessageID != "m-1" {
		t.Fatalf("correlation ids not carried: %+v", req.Context.DX.Actor)
	}
}

func TestInboundUncorrelatedCallbackRejected(t *testing.T) {
	// An on_* whose transaction we do not recognise must not build a request.
	if _, _, err := InboundRequest(InboundMsg{Method: "on_confirm", ParticipantID: "bpp-1", TransactionID: "txn-x", KnownTransaction: false}); err == nil {
		t.Fatal("an uncorrelated callback must be rejected, not silently authorized")
	}
}

func TestInboundUnsolicitedRequest(t *testing.T) {
	req, c, err := InboundRequest(InboundMsg{
		Method: "search", ParticipantID: "bap-2",
		LocalResourceType: "resource", LocalResourceID: "offer-9", OperationID: "beckn.search",
	})
	if err != nil {
		t.Fatal(err)
	}
	if c != CaseUnsolicited {
		t.Fatalf("a bare inbound request must be unsolicited, got %s", c)
	}
	// Authority is the participant + local policy; resource is the local object.
	if req.Resource.Type != "resource" || req.Resource.ID != "offer-9" {
		t.Fatalf("unsolicited request must target the local object: %+v", req.Resource)
	}
	if req.Action.Name != "query" {
		t.Fatalf("search action: %q", req.Action.Name)
	}
}

func TestInboundUnsolicitedNeedsLocalResource(t *testing.T) {
	if _, _, err := InboundRequest(InboundMsg{Method: "confirm", ParticipantID: "bap-2"}); err == nil {
		t.Fatal("an unsolicited request with no local resource must fail closed")
	}
}

func TestInboundUnmappedMethodRejected(t *testing.T) {
	if _, _, err := InboundRequest(InboundMsg{Method: "frobnicate", ParticipantID: "p"}); err == nil {
		t.Fatal("an unmapped method must be rejected")
	}
}
