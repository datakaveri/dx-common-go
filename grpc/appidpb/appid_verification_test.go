package appidpb

import (
	"bytes"
	"testing"

	"google.golang.org/protobuf/proto"
)

// These tests pin ROADMAP P1-10: the previously missing CheckItemAccessResponse
// fields round-trip, and an unknown future field (a later Java addition Go does
// not know yet) survives a decode/encode rather than being silently dropped.

func TestCheckItemAccessResponse_RoundTripsAllFields(t *testing.T) {
	orig := &CheckItemAccessResponse{
		Success:            true,
		ErrorCode:          "",
		Iid:                "urn:item:1",
		AccessPolicy:       "policy-1",
		ResourceServerJson: `[{"id":"rs"}]`,
		PoliciesJson:       "[]",
		HasOwnerAccess:     true, // field 7 — was missing from the Go copy
		HasAdminAccess:     true, // field 8 — was missing from the Go copy
	}

	wire, err := proto.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	got := &CheckItemAccessResponse{}
	if err := proto.Unmarshal(wire, got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !got.GetHasOwnerAccess() || !got.GetHasAdminAccess() {
		t.Fatalf("new fields lost: owner=%v admin=%v", got.GetHasOwnerAccess(), got.GetHasAdminAccess())
	}
	if got.GetIid() != "urn:item:1" || got.GetPoliciesJson() != "[]" || !got.GetSuccess() {
		t.Fatalf("existing fields corrupted on round trip: %+v", got)
	}
}

func TestCheckItemAccessResponse_PreservesUnknownFutureField(t *testing.T) {
	base, err := proto.Marshal(&CheckItemAccessResponse{HasOwnerAccess: true})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	// A field number Go's schema does not define yet — a future Java addition.
	// Tag = field 99, wire type 2 (bytes): (99<<3)|2 = 794 → varint 0x9a 0x06;
	// then length 3 and "xyz".
	future := append([]byte{0x9a, 0x06, 0x03}, []byte("xyz")...)
	raw := append(append([]byte{}, base...), future...)

	msg := &CheckItemAccessResponse{}
	if err := proto.Unmarshal(raw, msg); err != nil {
		t.Fatalf("an unknown future field must not break unmarshal: %v", err)
	}
	// proto3 retains unknown fields, so a re-marshal must still carry them —
	// otherwise a Go hop in the middle would strip data the two ends agreed on.
	out, err := proto.Marshal(msg)
	if err != nil {
		t.Fatalf("re-marshal: %v", err)
	}
	if !bytes.Contains(out, []byte("xyz")) {
		t.Error("unknown future field was dropped on re-marshal — forward-compatibility broken")
	}
}
