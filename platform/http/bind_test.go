package httpx

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/datakaveri/dx-common-go/platform/paging"
	"github.com/datakaveri/dx-common-go/platform/security/identity"
)

// The tests below pin the ONE ordering rule in bindStruct: the request body is
// decoded first and every other source overwrites it. Everything here fails if
// that is reversed, which is what it looked like for the first eleven migrated
// services.

type actorBodyReq struct {
	Actor
	Title string `json:"title"`
}

// TestBodyCannotOverwriteActor is a security regression test.
//
// identity.Subject's fields are promoted through the embedded Actor and carry
// no json tags, so encoding/json happily matches "id" and "roles" in a request
// body against the verified caller. When the Actor was filled BEFORE the decode,
// any authenticated caller could POST {"id":"<victim>"} and have the handler
// act as that user — route-level gating reads the context subject, so nothing
// upstream noticed.
func TestBodyCannotOverwriteActor(t *testing.T) {
	r := httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"title":"t","id":"attacker","roles":["cos_admin"]}`))
	r = r.WithContext(identity.With(r.Context(),
		identity.Subject{ID: "real-user", Roles: []string{"consumer"}}))

	got, err := bind[actorBodyReq](r)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.ID != "real-user" {
		t.Errorf("subject overwritten by body: ID = %q, want %q", got.ID, "real-user")
	}
	if len(got.Roles) != 1 || got.Roles[0] != "consumer" {
		t.Errorf("roles overwritten by body: %v, want [consumer]", got.Roles)
	}
	if got.Title != "t" {
		t.Errorf("Title = %q, want %q — the body must still bind", got.Title, "t")
	}
}

type optionalActorBodyReq struct {
	OptionalActor
	Title string `json:"title"`
}

// TestBodyCannotForgeOptionalActor is the anonymous half. OptionalActor is
// worse than Actor if left unwritten: a caller could send authenticated:true
// and an id of their choosing, and a handler that checks Authenticated — which
// is exactly what the type asks it to do — would believe it.
func TestBodyCannotForgeOptionalActor(t *testing.T) {
	r := httptest.NewRequest("POST", "/x",
		strings.NewReader(`{"title":"t","id":"attacker","authenticated":true}`))

	got, err := bind[optionalActorBodyReq](r)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.Authenticated {
		t.Error("Authenticated = true from the body: anonymity must overwrite it")
	}
	if got.ID != "" {
		t.Errorf("ID = %q from the body, want empty", got.ID)
	}
}

type precedenceReq struct {
	Actor
	paging.Request
	Thing string `path:"thing" json:"thing"`
	Kind  string `query:"kind" json:"kind"`
	Trace string `header:"X-Trace" json:"trace"`
}

// TestTaggedSourcesBeatBody pins the precedence bind documents — path → query
// → header → body. It read the other way round before: the body was decoded
// last and silently won over all three.
func TestTaggedSourcesBeatBody(t *testing.T) {
	r := httptest.NewRequest("POST", "/x?kind=from-query&page=2&size=5",
		strings.NewReader(`{"thing":"from-body","kind":"from-body","trace":"from-body","page":99}`))
	r.Header.Set("X-Trace", "from-header")
	r = r.WithContext(identity.With(r.Context(), identity.Subject{ID: "u-1"}))
	// The accessor rides the request context now, not a process-global.
	r = withPathValue(r, func(*http.Request, string) string { return "from-path" })

	got, err := bind[precedenceReq](r)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	for _, tc := range []struct{ name, got, want string }{
		{"path", got.Thing, "from-path"},
		{"query", got.Kind, "from-query"},
		{"header", got.Trace, "from-header"},
	} {
		if tc.got != tc.want {
			t.Errorf("%s source lost to body: got %q, want %q", tc.name, tc.got, tc.want)
		}
	}
	if got.Page != 2 || got.Size != 5 {
		t.Errorf("paging = page %d size %d, want 2/5 — the body must not set it", got.Page, got.Size)
	}
}

// TestBodyStillBindsWithoutActor guards the ordinary path: moving the decode
// earlier must not change what a plain request type binds.
func TestBodyStillBindsWithoutActor(t *testing.T) {
	type plain struct {
		Name  string `json:"name"`
		Count int    `json:"count"`
	}
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"name":"n","count":3}`))

	got, err := bind[plain](r)
	if err != nil {
		t.Fatalf("bind: %v", err)
	}
	if got.Name != "n" || got.Count != 3 {
		t.Errorf("got %+v, want {n 3}", got)
	}
}

// TestMissingSubjectStillFails keeps the 401 path intact: a protected request
// type must fail on the absent caller, not fall through with a zero Actor.
func TestMissingSubjectStillFails(t *testing.T) {
	r := httptest.NewRequest("POST", "/x", strings.NewReader(`{"title":"t"}`))

	if _, err := bind[actorBodyReq](r); err == nil {
		t.Fatal("bind succeeded with no subject on the context, want ErrNoSubject")
	}
}
