package auditing

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/datakaveri/dx-common-go/auth"
)

// F22: services mount Middleware on the route prefix, OUTSIDE the
// authentication resolver, so the record was built before the user was known
// and every Go audit record went out without user_id — a NOT NULL column in
// the audit table, so all of them were dropped. SetAction runs inside the
// handler, past authentication, and must fill the identity there.

const (
	f22User = "64183d05-743b-435c-8f2f-54b61039c65e"
	f22Org  = "3e2adc5e-7fa6-49d3-8f96-0ca538eee200"
)

// authenticate stands in for the auth resolver: it runs INSIDE the audit
// middleware, as in user/acl/catalogue.
func authenticate(user auth.DxUser, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
	})
}

func TestSetActionFillsIdentityWhenAuthRunsAfterMiddleware(t *testing.T) {
	user := auth.DxUser{ID: f22User, Name: "Go Applicant", Roles: []string{"consumer", "provider"},
		OrganisationID: f22Org, OrganisationName: "go-e2e-test-org"}
	var rec *Record
	h := Middleware(nil, OriginAAA)(authenticate(user, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec = SetAction(r.Context(), "ADD_ORG_MEMBER")
		w.WriteHeader(http.StatusCreated)
	})))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("POST", "/iudx/v2/auth/organisations/x/users", nil))

	if rec == nil {
		t.Fatal("no record")
	}
	if rec.UserID != f22User || rec.UserName != "Go Applicant" || rec.Role != "provider" {
		t.Fatalf("identity not filled: user_id=%q user_name=%q role=%q", rec.UserID, rec.UserName, rec.Role)
	}
	if rec.OrgID != f22Org || rec.OrgName != "go-e2e-test-org" {
		t.Fatalf("org not filled: %q %q", rec.OrgID, rec.OrgName)
	}
}

func TestSetActionKeepsIdentityTheMiddlewareSaw(t *testing.T) {
	first := auth.DxUser{ID: f22User, Name: "Seen By Middleware", Roles: []string{"consumer"}}
	other := auth.DxUser{ID: "8d1e5d7a-b051-4c93-9543-8a01db5e82e2", Name: "Someone Else", Roles: []string{"cos_admin"}}
	var rec *Record
	h := authenticate(first, Middleware(nil, OriginAAA)(authenticate(other, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec = SetAction(r.Context(), "A")
		w.WriteHeader(http.StatusOK)
	}))))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if rec.UserID != f22User || rec.UserName != "Seen By Middleware" {
		t.Fatalf("identity from the middleware was overwritten: %+v", rec)
	}
}

func TestSetActionWithoutUserLeavesIdentityEmpty(t *testing.T) {
	var rec *Record
	h := Middleware(nil, OriginAAA)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rec = SetAction(r.Context(), "A")
	}))
	h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "/x", nil))
	if rec.UserID != "" || rec.UserName != "" {
		t.Fatalf("anonymous request got an identity: %+v", rec)
	}
}
