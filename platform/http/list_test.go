package httpx

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/errors"
	"github.com/datakaveri/dx-common-go/platform/filter"
	"github.com/datakaveri/dx-common-go/platform/paging"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

var listTestSpec = filter.MustNew(
	[]filter.Field{
		filter.StringSet("entityType", "entity_type"),
		filter.StringSet("status", "status", filter.Allowed("pending", "granted", "rejected")),
	},
	filter.DefaultTime("createdAt", "created_at"),
)

type listTestReq struct {
	Actor
	params paging.Params
	fr     filter.Request
}

func (q *listTestReq) FilterSpec() *filter.Spec                  { return &listTestSpec }
func (q *listTestReq) SetList(p paging.Params, f filter.Request) { q.params, q.fr = p, f }

func listReqFor(t *testing.T, rawQuery string) *http.Request {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/?"+rawQuery, nil)
	return r.WithContext(identity.With(r.Context(), identity.Subject{ID: "u-1"}))
}

// The headline acceptance: the exact multi-value + status + sort shape that
// currently 400s must bind, WITHOUT the ListContract having to re-derive the
// actor (which normal binding still populated).
func TestListContractBindsFailingURLShape(t *testing.T) {
	raw := "page=1&size=10&sort=createdAt:desc" +
		"&entityType=Private+Limited+Company&entityType=Public+Limited+Company" +
		"&entityType=Partnership+Firm&entityType=Sole+Proprietorship" +
		"&entityType=Limited+Liability+Partnership&entityType=Government+Organization" +
		"&entityType=Non-Profit+Organization&status=pending"

	got, err := bind[listTestReq](listReqFor(t, raw))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.ID != "u-1" {
		t.Errorf("actor not bound (%q) — the contract must not replace normal binding", got.ID)
	}
	if got.params.Size != 10 || got.params.Page != 1 {
		t.Errorf("paging = %d/%d, want 1/10", got.params.Page, got.params.Size)
	}
	if len(got.params.Sort) != 1 || got.params.Sort[0].Field != "createdAt" || got.params.Sort[0].Dir != paging.Desc {
		t.Errorf("sort = %+v, want createdAt:desc", got.params.Sort)
	}
	if n := len(got.fr.Exact()["entity_type"]); n != 7 {
		t.Errorf("entity_type values = %d, want 7", n)
	}
	if s := got.fr.Exact()["status"]; len(s) != 1 || s[0] != "pending" {
		t.Errorf("status = %v, want [pending]", s)
	}
}

// status alone must be a 200 (the legacy double-allowlist made even this a 400).
func TestListContractStatusAlone(t *testing.T) {
	got, err := bind[listTestReq](listReqFor(t, "status=pending"))
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if s := got.fr.Exact()["status"]; len(s) != 1 || s[0] != "pending" {
		t.Errorf("status = %v, want [pending]", s)
	}
}

func TestListContractRejectsUnknownParam(t *testing.T) {
	_, err := bind[listTestReq](listReqFor(t, "entityType=A&notAThing=x"))
	if err == nil {
		t.Fatal("unknown query parameter accepted; it would silently return an unfiltered page")
	}
	if !errors.IsValidation(err) {
		t.Errorf("err = %v, want a validation (400) error", err)
	}
}

func TestListContractRejectsBadEnum(t *testing.T) {
	_, err := bind[listTestReq](listReqFor(t, "status=nonsense"))
	if err == nil || !errors.IsValidation(err) {
		t.Fatalf("bad status enum: err = %v, want validation error", err)
	}
}
