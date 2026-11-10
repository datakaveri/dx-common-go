package appid

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"

	"github.com/datakaveri/dx-common-go/auth"
	"github.com/datakaveri/dx-common-go/grpc/appidpb"
)

// F58-r: Java AuthenticationHandler.postAuthentication + GrpcDelegationResolver.

type fakeResolver struct {
	appidpb.UnimplementedAppIdVerificationServiceServer
	mu    sync.Mutex
	calls []*appidpb.ResolveDelegationRequest
	resp  *appidpb.ResolveDelegationResponse
}

func (f *fakeResolver) ResolveDelegation(_ context.Context, req *appidpb.ResolveDelegationRequest) (*appidpb.ResolveDelegationResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, req)
	return f.resp, nil
}

func (f *fakeResolver) n() int { f.mu.Lock(); defer f.mu.Unlock(); return len(f.calls) }

func delegatorResp() *appidpb.ResolveDelegationResponse {
	return &appidpb.ResolveDelegationResponse{Success: true, Sub: "delegator-1", OrganisationId: "org-d",
		OrganisationName: "Org D", Roles: []string{"provider", "consumer"}, Scopes: []string{"data-access"},
		Email: "d@x.org", Name: "Dee Lee", AccountEnabled: true, DelegateeSub: "caller-1"}
}

func resolverClient(t *testing.T, f *fakeResolver) *Client {
	t.Helper()
	lis := serveResolver(t, f)
	c, err := NewClient(Config{Enabled: true, Address: lis, WorkloadDestination: "dx-user-go"})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c.WithWorkload(&staticTokens{})
}

func through(c *Client, user *auth.DxUser, did string) (*httptest.ResponseRecorder, *auth.DxUser) {
	var seen *auth.DxUser
	h := DelegationMiddleware(c)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		if u, ok := auth.UserFromCtx(r.Context()); ok {
			seen = &u
		}
	}))
	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	if did != "" {
		req.Header.Set("did", did)
	}
	if user != nil {
		req = req.WithContext(auth.WithUser(req.Context(), *user))
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, seen
}

// Bearer + did: the request continues as the delegator — Java's
// resolve(delegatorSub = did, delegateeSub = JWT sub).
func TestDelegationReplacesIdentityWithDelegator(t *testing.T) {
	f := &fakeResolver{resp: delegatorResp()}
	c := resolverClient(t, f)
	rec, u := through(c, &auth.DxUser{ID: "caller-1", Roles: []string{"delegate"}}, "delegator-1")
	if rec.Code != http.StatusOK || u == nil {
		t.Fatalf("code %d user %+v", rec.Code, u)
	}
	if f.calls[0].GetDelegatorSub() != "delegator-1" || f.calls[0].GetDelegateeSub() != "caller-1" {
		t.Fatalf("request = %+v", f.calls[0])
	}
	if u.ID != "delegator-1" || u.DelegateeID != "caller-1" || u.OrganisationID != "org-d" ||
		u.OrganisationName != "Org D" || u.Email != "d@x.org" || u.Name != "Dee Lee" ||
		!slices.Equal(u.Roles, []string{"provider", "consumer"}) ||
		len(u.Scopes) != 1 || u.Scopes[0].Scope != "data-access" || u.AppID != "" {
		t.Fatalf("user = %+v", u)
	}
}

// Basic + did: the app owner is the delegatee and the app id stays.
func TestDelegationKeepsTheCallersApp(t *testing.T) {
	f := &fakeResolver{resp: delegatorResp()}
	c := resolverClient(t, f)
	_, u := through(c, &auth.DxUser{ID: "caller-1", AppID: "app-9"}, "delegator-1")
	if u == nil || u.ID != "delegator-1" || u.AppID != "app-9" || u.DelegateeID != "caller-1" {
		t.Fatalf("user = %+v", u)
	}
	// And from the cache.
	_, u = through(c, &auth.DxUser{ID: "caller-1", AppID: "app-9"}, "delegator-1")
	if u == nil || u.AppID != "app-9" || f.n() != 1 {
		t.Fatalf("user = %+v calls %d", u, f.n())
	}
}

func TestDelegationRefusedIs403(t *testing.T) {
	f := &fakeResolver{resp: &appidpb.ResolveDelegationResponse{Success: false, ErrorCode: "EXPIRED"}}
	c := resolverClient(t, f)
	rec, u := through(c, &auth.DxUser{ID: "caller-1"}, "delegator-1")
	if rec.Code != http.StatusForbidden || u != nil {
		t.Fatalf("code %d user %+v body %s", rec.Code, u, rec.Body)
	}
	_, err := c.ResolveDelegator(context.Background(), "delegator-1", auth.DxUser{ID: "caller-1"})
	var de *DelegationError
	if !errors.Is(err, ErrNoActiveDelegation) || !errors.As(err, &de) || de.Code != "EXPIRED" {
		t.Fatalf("err = %v", err)
	}
	// Refusals are not cached.
	if f.n() != 2 {
		t.Fatalf("calls = %d", f.n())
	}
}

// No did, a blank did, or no authenticated caller: nothing to resolve.
func TestDelegationPassThrough(t *testing.T) {
	f := &fakeResolver{resp: delegatorResp()}
	c := resolverClient(t, f)
	if _, u := through(c, &auth.DxUser{ID: "caller-1"}, ""); u == nil || u.ID != "caller-1" {
		t.Fatalf("no did: %+v", u)
	}
	if _, u := through(c, &auth.DxUser{ID: "caller-1"}, "   "); u == nil || u.ID != "caller-1" {
		t.Fatalf("blank did: %+v", u)
	}
	if rec, u := through(c, nil, "delegator-1"); rec.Code != http.StatusOK || u != nil {
		t.Fatalf("anonymous: %d %+v", rec.Code, u)
	}
	if f.n() != 0 {
		t.Fatalf("calls = %d", f.n())
	}
}

func TestDelegationTransportFailureIs500(t *testing.T) {
	c, err := NewClient(Config{Enabled: true, Address: "127.0.0.1:1", WorkloadDestination: "dx-user-go",
		CallTimeout: 300 * time.Millisecond})
	if err != nil {
		t.Fatalf("new: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })
	c.WithWorkload(&staticTokens{})
	rec, u := through(c, &auth.DxUser{ID: "caller-1"}, "delegator-1")
	if rec.Code != http.StatusInternalServerError || u != nil {
		t.Fatalf("code %d user %+v", rec.Code, u)
	}
}

// Java's cache: 2 minutes after write, per delegator:delegatee.
func TestDelegationCacheTTL(t *testing.T) {
	f := &fakeResolver{resp: delegatorResp()}
	c := resolverClient(t, f)
	now := time.Now()
	c.delegations.now = func() time.Time { return now }
	caller := auth.DxUser{ID: "caller-1"}
	for i := 0; i < 2; i++ {
		if _, err := c.ResolveDelegator(context.Background(), "delegator-1", caller); err != nil {
			t.Fatal(err)
		}
	}
	if f.n() != 1 {
		t.Fatalf("calls = %d, want 1 (cached)", f.n())
	}
	if _, err := c.ResolveDelegator(context.Background(), "delegator-1", auth.DxUser{ID: "caller-2"}); err != nil {
		t.Fatal(err)
	}
	now = now.Add(2*time.Minute + time.Second)
	if _, err := c.ResolveDelegator(context.Background(), "delegator-1", caller); err != nil {
		t.Fatal(err)
	}
	if f.n() != 3 {
		t.Fatalf("calls = %d, want 3", f.n())
	}
}

func TestDelegationCacheBounded(t *testing.T) {
	c := &delegationCache{now: time.Now}
	for i := 0; i < delegationCacheMax+50; i++ {
		c.put(string(rune('a'+i%26))+time.Duration(i).String(), auth.DxUser{ID: "x"})
	}
	if len(c.entries) > delegationCacheMax {
		t.Fatalf("entries = %d", len(c.entries))
	}
}

func serveResolver(t *testing.T, f *fakeResolver) string {
	t.Helper()
	lis, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	s := grpc.NewServer()
	appidpb.RegisterAppIdVerificationServiceServer(s, f)
	go func() { _ = s.Serve(lis) }()
	t.Cleanup(s.Stop)
	return lis.Addr().String()
}
