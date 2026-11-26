package auditing

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"

	"github.com/datakaveri/dx-common-go/auth"
)

// javaSchemaKeys is the authoritative field list from the Java
// UserActivityAuditSchema constants (dx-controlplane auditing/v2), plus the
// two legacy v1 keys delegator_id/delegator_role that older publishers still
// send (the v2 consumer ignores them). The Go Record must never emit a key
// outside this set or the Java entity parser may reject it.
var javaSchemaKeys = map[string]struct{}{
	"id": {}, "user_id": {}, "user_name": {}, "org_id": {}, "org_name": {},
	"org_type": {}, "app_id": {}, "role": {}, "issuer": {},
	"delegate_id": {}, "actor_type": {},
	"delegator_id": {}, "delegator_role": {},
	"api": {}, "method": {}, "action": {}, "origin_server": {},
	"asset_id": {}, "asset_name": {}, "asset_sort_discription": {},
	"asset_type": {}, "asset_access_policy": {}, "asset_org_id": {},
	"asset_org_name": {}, "asset_org_type": {}, "asset_provider_id": {},
	"asset_provider_name": {},
	"size_bytes":          {}, "amount": {}, "request_id": {},
	"log_type": {}, "sandbox_type": {},
	"ip_address": {}, "user_agent": {}, "created_at": {}, "context": {},
}

func fullRecord() *Record {
	id := uuid.NewString()
	return &Record{
		ID: id, UserID: id, UserName: "u", Role: "provider", Issuer: "kc",
		OrgID: id, OrgName: "o", OrgType: "t", AppID: id,
		DelegateID: id, ActorType: ActorDelegate,
		DelegatorID: id, DelegatorRole: "consumer",
		API: "/x", Method: "POST", Action: "A", OriginServer: "ACL",
		AssetID: id, AssetName: "a", AssetShortDescription: "d",
		AssetType: "DATABANK", AssetAccessPolicy: "p", AssetOrgID: id,
		AssetOrgName: "n", AssetOrgType: "y", AssetProviderID: id,
		AssetProviderName: "pn",
		SizeBytes:         1, Amount: "1.5", RequestID: id,
		LogType: LogTypeUserAction, SandboxType: "s",
		IPAddress: "1.2.3.4", UserAgent: "ua",
		CreatedAt: "2026-06-12T10:00:00.000000",
		Context:   json.RawMessage(`{"k":"v"}`),
	}
}

func TestRecordKeysMatchJavaSchema(t *testing.T) {
	b, err := json.Marshal(fullRecord())
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	if len(m) != len(javaSchemaKeys) {
		t.Fatalf("expected all %d Java schema keys, got %d", len(javaSchemaKeys), len(m))
	}
	for k := range m {
		if _, ok := javaSchemaKeys[k]; !ok {
			t.Fatalf("key %q is not in the Java UserActivityAuditSchema", k)
		}
	}
}

func TestBaseRecordSkipsInvalidUUIDs(t *testing.T) {
	rec := BaseRecord(auth.DxUser{
		ID:             "svc:gateway", // not a UUID — must be omitted
		Name:           "Gateway",
		Roles:          []string{"service"},
		OrganisationID: "not-a-uuid",
	}, "GW", "/p", "GET", "1.1.1.1", "ua", "also-not-uuid")
	if rec.UserID != "" || rec.OrgID != "" || rec.RequestID != "" {
		t.Fatalf("non-UUID identity fields must be omitted: %+v", rec)
	}
	if rec.CreatedAt == "" || rec.ID == "" {
		t.Fatal("id and created_at must always be set")
	}
}

func TestEffectiveRole(t *testing.T) {
	if got := EffectiveRole([]string{"consumer", "org_admin"}); got != "org_admin" {
		t.Fatalf("precedence: got %s", got)
	}
	if got := EffectiveRole([]string{"weird"}); got != "weird" {
		t.Fatalf("fallback: got %s", got)
	}
	if got := EffectiveRole(nil); got != "" {
		t.Fatalf("empty: got %q", got)
	}
}

func TestMiddlewareOptIn(t *testing.T) {
	// No publisher: must still inject a record so handlers can enrich.
	var captured *Record
	h := Middleware(nil, "TEST")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		captured = SetAction(r.Context(), "DO_THING")
		w.WriteHeader(http.StatusCreated)
	}))
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest("POST", "/things", nil))
	if captured == nil || captured.Action != "DO_THING" {
		t.Fatalf("record not injected/enriched: %+v", captured)
	}
	if captured.Method != "POST" || captured.API != "/things" || captured.OriginServer != "TEST" {
		t.Fatalf("base fields wrong: %+v", captured)
	}
}

func TestSetActionNilSafe(t *testing.T) {
	// Without middleware: must not panic.
	req := httptest.NewRequest("GET", "/x", nil)
	if r := SetAction(req.Context(), "A"); r != nil {
		t.Fatal("expected nil record without middleware")
	}
}

// Java v2 ActorType: exactly SELF, DELEGATE, APP.
func TestActorTypesAreJavas(t *testing.T) {
	if ActorSelf != "SELF" || ActorDelegate != "DELEGATE" || ActorApp != "APP" {
		t.Fatalf("actor types = %q %q %q", ActorSelf, ActorDelegate, ActorApp)
	}
}
